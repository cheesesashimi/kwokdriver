package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/cheesesashimi/kwokdriver/pkg/data"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"k8s.io/klog/v2"
)

type KwokImageOpts struct {
	ReleaseImage string
	KWOKImage    string
}

type KwokImageBuilder struct {
	opts KwokImageOpts
}

type buildData struct {
	*data.ImageMetadata
	buildCtx     io.ReadSeeker
	buildCtxHash string
}

func newData(i *data.ImageMetadata) (*buildData, error) {
	klog.V(1).InfoS("Preparing KWOK image build data", "releaseVersion", i.ReleaseVersion)
	bd := &buildData{ImageMetadata: i}

	imagesJSONBytes, err := json.Marshal(bd.Images)
	if err != nil {
		return nil, fmt.Errorf("cuold not marshal images into JSON: %w", err)
	}

	mdJSONBytes, err := json.Marshal(i)
	if err != nil {
		return nil, err
	}

	dynamicFiles := map[string][]byte{
		"images.json": imagesJSONBytes,
		"build.json":  mdJSONBytes,
	}

	buildCtx, buildCtxHash, err := getBuildContext(dynamicFiles)
	if err != nil {
		return nil, fmt.Errorf("could not get build context: %w", err)
	}

	bd.buildCtx = buildCtx
	bd.buildCtxHash = buildCtxHash

	return bd, nil
}

func NewKwokImageBuilder(o KwokImageOpts) *KwokImageBuilder {
	return &KwokImageBuilder{opts: o}
}

func hasAllKeys(keys []string, in map[string]string) bool {
	for _, key := range keys {
		if _, ok := in[key]; !ok {
			return false
		}
	}

	return true
}

func hasAllKeysAndValues(target, subset map[string]string) bool {
	for k, v := range subset {
		val, ok := target[k]
		if !ok {
			return false
		}

		if val != v {
			return false
		}
	}

	return true
}

func (k *KwokImageBuilder) isBuildNeeded(ctx context.Context, pullspec string) (bool, *buildData, error) {
	klog.V(1).InfoS("Checking whether KWOK image build is needed", "image", pullspec)
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false, nil, err
	}

	list, err := provider.Client().ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return false, nil, err
	}

	expectedLabelKeys := []string{
		"KWOK_BASE_IMAGE",
		"OPENSHIFT_RELEASE_IMAGE",
		"BUILD_CONTEXT_HASH",
	}

	for _, c := range list.Items {
		if !slices.Contains(c.RepoTags, pullspec) {
			continue
		}

		if !hasAllKeys(expectedLabelKeys, c.Labels) {
			continue
		}

		data, err := k.getDataFromKwokImage(ctx, pullspec)
		if err != nil {
			return false, nil, err
		}

		expectedLabels := map[string]string{
			"KWOK_BASE_IMAGE":         k.opts.KWOKImage,
			"OPENSHIFT_RELEASE_IMAGE": k.opts.ReleaseImage,
			"BUILD_CONTEXT_HASH":      data.buildCtxHash,
		}

		if !hasAllKeysAndValues(c.Labels, expectedLabels) {
			continue
		}

		return false, data, nil
	}

	klog.V(1).InfoS("KWOK image build is needed", "image", pullspec)
	return true, nil, nil
}

func (k *KwokImageBuilder) getDataFromReleaseImage(ctx context.Context) (*buildData, error) {
	klog.InfoS("Loading build data from release image", "image", k.opts.ReleaseImage)
	data, err := data.NewFromReleaseImage(ctx, k.opts.ReleaseImage)
	if err != nil {
		return nil, err
	}

	return newData(data)
}

func (k *KwokImageBuilder) getDataFromKwokImage(ctx context.Context, pullspec string) (*buildData, error) {
	data, err := data.NewFromKWOKClusterImage(ctx, pullspec)
	if err != nil {
		return nil, err
	}

	return newData(data)
}

func (k *KwokImageBuilder) Build(ctx context.Context, finalPullspec string) (*data.ImageMetadata, error) {
	isNeeded, buildData, err := k.isBuildNeeded(ctx, finalPullspec)
	if err != nil {
		return nil, err
	}

	if !isNeeded {
		return buildData.ImageMetadata, nil
	}

	klog.InfoS("Building KWOK image", "image", finalPullspec, "releaseImage", k.opts.ReleaseImage, "kwokImage", k.opts.KWOKImage)

	split := strings.Split(finalPullspec, ":")

	data, err := k.getDataFromReleaseImage(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to load build data from release image", "image", k.opts.ReleaseImage)
		return nil, err
	}

	osImage, err := data.LookupComponent("rhel-coreos-10")
	if err != nil {
		return nil, fmt.Errorf("could not get OS image pullspec: %w", err)
	}

	extImage, err := data.LookupComponent("rhel-coreos-10-extensions")
	if err != nil {
		return nil, fmt.Errorf("could not get extensions image pullspec: %w", err)
	}

	df := testcontainers.FromDockerfile{
		Dockerfile:     "Containerfile",
		ContextArchive: data.buildCtx,
		BuildArgs: map[string]*string{
			"KWOK_IMAGE":              &k.opts.KWOKImage,
			"MCO_VERSION_HASH":        &data.McoReleaseHash,
			"OPENSHIFT_RELEASE_IMAGE": &k.opts.ReleaseImage,
			"RELEASE_IMAGE":           &k.opts.ReleaseImage,
			"RELEASE_VERSION":         &data.ReleaseVersion,
			"RHEL_COREOS_IMAGE":       &osImage.From.Name,
			"RHEL_COREOS_EXT_IMAGE":   &extImage.From.Name,
			"BUILD_CONTEXT_HASH":      &data.buildCtxHash,
		},
		KeepImage:      true,
		BuildLogWriter: os.Stderr,
		Repo:           split[0],
		Tag:            split[1],
	}

	con, err := testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(df),
		testcontainers.WithNoStart(),
		testcontainers.WithProvider(testcontainers.ProviderPodman),
	)
	if err != nil {
		klog.ErrorS(err, "Failed to build KWOK image", "image", finalPullspec)
		return nil, err
	}

	if err := con.Terminate(ctx); err != nil {
		klog.ErrorS(err, "Failed to remove build container", "image", finalPullspec)
		return nil, err
	}

	klog.InfoS("Built KWOK image", "image", finalPullspec)
	return data.ImageMetadata, nil
}
