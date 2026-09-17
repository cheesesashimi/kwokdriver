package kwokdriver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	imagesv1 "github.com/openshift/api/image/v1"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type KwokImageOpts struct {
	ReleaseImage string
	KWOKImage    string
}

type KwokImageBuilder struct {
	opts KwokImageOpts
}

type BuildData struct {
	Imagestream    *imagesv1.ImageStream `json:"imagestream,omitempty"`
	ReleaseImage   string                `json:"releaseImage,omitempty"`
	ReleaseVersion string                `json:"releaseVersion,omitempty"`
	McoReleaseHash string                `json:"mcoReleaseHash,omitempty"`
	BuildCtx       io.ReadSeeker         `json:"-"`
	BuildCtxHash   string                `json:"-"`
	Images         map[string]string     `json:"images"`
}

func NewKwokImageBuilder(o KwokImageOpts) *KwokImageBuilder {
	return &KwokImageBuilder{opts: o}
}

func (k *KwokImageBuilder) isBuildNeeded(ctx context.Context, data *BuildData, pullspec string) (bool, error) {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false, err
	}

	list, err := provider.Client().ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return false, err
	}

	for _, c := range list.Items {
		if k.isMatchingImage(c, data, pullspec) {
			return false, nil
		}
	}

	return true, nil
}

func (k *KwokImageBuilder) isMatchingImage(c image.Summary, data *BuildData, pullspec string) bool {
	if !slices.Contains(c.RepoTags, pullspec) {
		return false
	}

	expectedLabels := map[string]string{
		"KWOK_BASE_IMAGE":         k.opts.KWOKImage,
		"OPENSHIFT_RELEASE_IMAGE": k.opts.ReleaseImage,
		"BUILD_CONTEXT_HASH":      data.BuildCtxHash,
	}

	for name, value := range expectedLabels {
		val, ok := c.Labels[name]
		if !ok {
			return false
		}

		if value != val {
			return false
		}
	}

	return true
}

func (k *KwokImageBuilder) GetBuildData(ctx context.Context) (*BuildData, error) {
	imagestream, releaseVersion, err := k.getReleaseImageData(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get imagestream or release version: %w", err)
	}

	mcoReleaseHash, err := k.getMCOReleaseHash(ctx, imagestream)
	if err != nil {
		return nil, fmt.Errorf("could not get mco release hash: %w", err)
	}

	images, err := getImagesFromImageStream(imagestream, releaseVersion)
	if err != nil {
		return nil, fmt.Errorf("could not get images from imagestream: %w", err)
	}

	imagesJSONBytes, err := json.Marshal(images)
	if err != nil {
		return nil, fmt.Errorf("cuold not marshal images into JSON: %w", err)
	}

	bd := &BuildData{
		Imagestream:    imagestream,
		ReleaseImage:   k.opts.ReleaseImage,
		ReleaseVersion: releaseVersion,
		McoReleaseHash: mcoReleaseHash,
		//		BuildCtx:       buildCtx,
		//		BuildCtxHash:   buildCtxHash,
		Images: images,
	}

	bdJSONBytes, err := json.Marshal(bd)

	dynamicFiles := map[string][]byte{
		"images.json": imagesJSONBytes,
		"build.json":  bdJSONBytes,
	}

	buildCtx, buildCtxHash, err := getBuildContext(dynamicFiles)
	if err != nil {
		return nil, fmt.Errorf("could not get build context: %w", err)
	}

	bd.BuildCtx = buildCtx
	bd.BuildCtxHash = buildCtxHash

	return bd, nil
}

func (k *KwokImageBuilder) Build(ctx context.Context, pullspec string) (*BuildData, error) {
	data, err := k.GetBuildData(ctx)
	if err != nil {
		return nil, err
	}

	isNeeded, err := k.isBuildNeeded(ctx, data, pullspec)
	if err != nil {
		return nil, err
	}

	if !isNeeded {
		return data, nil
	}

	df := testcontainers.FromDockerfile{
		Dockerfile:     "Containerfile",
		ContextArchive: data.BuildCtx,
		BuildArgs: map[string]*string{
			"KWOK_IMAGE":              &k.opts.KWOKImage,
			"MCO_VERSION_HASH":        &data.McoReleaseHash,
			"OPENSHIFT_RELEASE_IMAGE": &k.opts.ReleaseImage,
			"RELEASE_IMAGE":           &k.opts.ReleaseImage,
			"RELEASE_VERSION":         &data.ReleaseVersion,
			"RHEL_COREOS_EXT_IMAGE":   &lookupComponentFromImageStream(data.Imagestream, "rhel-coreos-10-extensions").From.Name,
			"RHEL_COREOS_IMAGE":       &lookupComponentFromImageStream(data.Imagestream, "rhel-coreos-10").From.Name,
			"BUILD_CONTEXT_HASH":      &data.BuildCtxHash,
		},
		KeepImage:      true,
		BuildLogWriter: os.Stderr,
		Repo:           "localhost/kwok",
		Tag:            "latest",
	}

	con, err := testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(df),
		testcontainers.WithNoStart(),
		testcontainers.WithProvider(testcontainers.ProviderPodman),
	)
	if err != nil {
		return nil, err
	}

	if err := con.Terminate(ctx); err != nil {
		return nil, err
	}

	return data, nil
}

func (k *KwokImageBuilder) getReleaseImageData(ctx context.Context) (*imagesv1.ImageStream, string, error) {
	con, err := testcontainers.Run(
		ctx,
		k.opts.ReleaseImage,
		testcontainers.WithNoStart(),
	)
	if err != nil {
		return nil, "", err
	}

	defer con.Stop(ctx, nil)

	is := &imagesv1.ImageStream{}

	if err := k.decodeJSONFromContainer(ctx, con, is, "/release-manifests/image-references"); err != nil {
		return nil, "", err
	}

	type relVersion struct {
		Version string `json:"version"`
	}

	rv := &relVersion{}

	if err := k.decodeJSONFromContainer(ctx, con, rv, "/release-manifests/release-metadata"); err != nil {
		return nil, "", err
	}

	return is, rv.Version, nil
}

func (k *KwokImageBuilder) getMCOReleaseHash(ctx context.Context, is *imagesv1.ImageStream) (string, error) {
	version, err := k.getMCOReleaseHashFromContainer(ctx, is)
	if err != nil {
		return "", err
	}

	re := regexp.MustCompile(`(?m)[a-f0-9]{40}$`)

	match := re.FindString(string(version))
	if match == "" {
		return "", fmt.Errorf("could not get machine-config-operator version hash from %q", string(version))
	}

	return strings.TrimSpace(match), nil
}

func (k *KwokImageBuilder) getMCOReleaseHashFromContainer(ctx context.Context, is *imagesv1.ImageStream) ([]byte, error) {
	mcoImage := lookupComponentFromImageStream(is, "machine-config-operator")
	if mcoImage == nil {
		return nil, fmt.Errorf("could not get machine-config-operator image")
	}

	mcoContainer, err := testcontainers.Run(
		ctx,
		mcoImage.From.Name,
		testcontainers.WithEntrypoint("/usr/bin/machine-config-operator", "version"),
		testcontainers.WithWaitStrategy(wait.ForExit()),
		testcontainers.WithProvider(testcontainers.ProviderPodman),
	)
	if err != nil {
		return nil, err
	}

	defer func() {
		mcoContainer.Stop(ctx, nil)
	}()

	reader, err := mcoContainer.Logs(ctx)
	if err != nil {
		return nil, err
	}

	defer reader.Close()

	return io.ReadAll(reader)
}

func (k *KwokImageBuilder) decodeJSONFromContainer(ctx context.Context, con testcontainers.Container, out interface{}, path string) error {
	fileRC, err := con.CopyFileFromContainer(ctx, path)
	if err != nil {
		return err
	}

	defer fileRC.Close()

	return json.NewDecoder(fileRC).Decode(out)
}
