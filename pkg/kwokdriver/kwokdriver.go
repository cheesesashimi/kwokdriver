package kwokdriver

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	imagesv1 "github.com/openshift/api/image/v1"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

//go:embed image
var buildContextFS embed.FS

type Opts struct {
	// The OpenShift release image pullspec to use.
	ReleaseImage string
	// The upstream KWOK all-in-one image to use.
	KWOKImage string
	// The prepared KWOK cluster image to use. NOTE: If this supplied, this image will be used as-is.
	KWOKClusterImage string
	// This allows an on-disk MCO binary to be used instead of the one in the container image.
	MCOBinaryPath string
	// This allows an on-disk MCC binary to be used instead of the one in the container image.
	MCCBinaryPath string
}

type KWOKContainerResult struct {
	KWOKContainer  testcontainers.Container
	MCOContainer   testcontainers.Container
	MCCContainer   testcontainers.Container
	Network        *testcontainers.DockerNetwork
	ImageStream    *imagesv1.ImageStream
	MCOVersionHash string
	ReleaseVersion string
	Kubeconfigs
}

func (k *KWOKContainerResult) LookupComponentImage(name string) *imagesv1.TagReference {
	return lookupComponentFromImageStream(k.ImageStream, name)
}

func (k *KWOKContainerResult) ImagesJSON() ([]byte, error) {
	return getImagesJSONFromImageStream(k.ImageStream, k.ReleaseVersion)
}

func lookupComponentFromImageStream(is *imagesv1.ImageStream, name string) *imagesv1.TagReference {
	for _, tag := range is.Spec.Tags {
		if tag.Name == name {
			return tag.DeepCopy()
		}
	}

	return nil
}

type KwokDriver struct {
	opts                   Opts
	provider               testcontainers.GenericProvider
	network                *testcontainers.DockerNetwork
	testLogConsumerFactory *testLogConsumerFactory
}

func NewKwokDriver(o Opts) *KwokDriver {
	nw, err := network.New(context.TODO(), network.WithLabels(map[string]string{
		"testcontainers": "true",
	}))
	if err != nil {
		panic(err)
	}

	return &KwokDriver{
		opts:    o,
		network: nw,
	}
}

func (k *KwokDriver) newLogConsumers(name string) []testcontainers.LogConsumer {
	flc, err := newFileLogConsumer(name + ".log")
	if err != nil {
		panic(err)
	}

	return []testcontainers.LogConsumer{
		k.testLogConsumerFactory.newLogConsumerForComponent(name),
		flc,
	}
}

func (k *KwokDriver) buildKwokImage(ctx context.Context, t *testing.T) (testcontainers.Container, error) {
	imagestream, releaseVersion, err := k.getReleaseImageData(ctx, t)
	if err != nil {
		return nil, err
	}

	mcoReleaseHash, err := k.getMCOReleaseHash(ctx, t, imagestream)
	if err != nil {
		return nil, err
	}

	imagesJSON, err := getImagesJSONFromImageStream(imagestream, releaseVersion)
	if err != nil {
		return nil, err
	}

	dynamicFiles := map[string][]byte{
		"images.json": imagesJSON,
	}

	buildCtx, _, err := getBuildContext(dynamicFiles)
	if err != nil {
		return nil, err
	}

	df := testcontainers.FromDockerfile{
		Dockerfile:     "Containerfile",
		ContextArchive: buildCtx,
		BuildArgs: map[string]*string{
			"KWOK_IMAGE":              &k.opts.KWOKImage,
			"MCO_VERSION_HASH":        &mcoReleaseHash,
			"OPENSHIFT_RELEASE_IMAGE": &k.opts.ReleaseImage,
			"RELEASE_IMAGE":           &k.opts.ReleaseImage,
			"RELEASE_VERSION":         &releaseVersion,
			"RHEL_COREOS_EXT_IMAGE":   &lookupComponentFromImageStream(imagestream, "rhel-coreos-10-extensions").From.Name,
			"RHEL_COREOS_IMAGE":       &lookupComponentFromImageStream(imagestream, "rhel-coreos-10").From.Name,
		},
		KeepImage:      true,
		BuildLogWriter: os.Stderr,
		Repo:           "localhost/kwok",
		Tag:            "latest",
	}

	return testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(df),
		testcontainers.WithNoStart(),
		testcontainers.WithProvider(testcontainers.ProviderPodman),
		testcontainers.WithLogConsumers(k.newLogConsumers("kwok-api-server")...),
	)
}

func (k *KwokDriver) startKwokContainer(ctx context.Context, t *testing.T) (*KWOKContainerResult, error) {
	testcontainers.CleanupNetwork(t, k.network)

	k.testLogConsumerFactory = newTestLogConsumerFactory(t)

	imagestream, releaseVersion, err := k.getReleaseImageData(ctx, t)
	if err != nil {
		return nil, err
	}

	mcoReleaseHash, err := k.getMCOReleaseHash(ctx, t, imagestream)
	if err != nil {
		return nil, err
	}

	if k.opts.KWOKClusterImage == "" {
		if _, err := k.buildKwokImage(ctx, t); err != nil {
			return nil, fmt.Errorf("could not build KWOK image: %w", err)
		}

		k.opts.KWOKClusterImage = "localhost/kwok:latest"
	}

	readyMsg := "KWOK OCP Cluster is ready"

	con, err := testcontainers.Run(
		ctx,
		k.opts.KWOKClusterImage,
		testcontainers.WithWaitStrategy(
			wait.ForLog(readyMsg),
			wait.ForExposedPort(),
		),
		testcontainers.WithExposedPorts("8080"),
		testcontainers.WithLogConsumers(k.newLogConsumers("kwok-api-server")...),
		testcontainers.WithEnv(map[string]string{
			"READY_MSG": readyMsg,
		}),
		network.WithNetwork([]string{}, k.network),
		testcontainers.WithLabels(map[string]string{
			"testcontainers": "true",
		}),
		testcontainers.WithProvider(testcontainers.ProviderPodman),
	)

	testcontainers.CleanupContainer(t, con)

	if err != nil {
		return nil, err
	}

	kcfgs, err := newKubeconfigs(ctx, con, k.network)
	if err != nil {
		return nil, err
	}

	return &KWOKContainerResult{
		KWOKContainer:  con,
		Network:        k.network,
		ImageStream:    imagestream,
		MCOVersionHash: mcoReleaseHash,
		Kubeconfigs:    *kcfgs,
		ReleaseVersion: releaseVersion,
	}, nil
}

func (k *KwokDriver) SetupKWOKImage(ctx context.Context, t *testing.T) (*KWOKContainerResult, error) {
	kcr, err := k.startKwokContainer(ctx, t)
	if err != nil {
		return nil, err
	}

	mcoContainer, err := k.startMCOContainer(ctx, kcr)
	if err != nil {
		return nil, err
	}

	kcr.MCOContainer = mcoContainer

	mccContainer, err := k.startMCCContainer(ctx, kcr)
	if err != nil {
		return nil, err
	}

	kcr.MCCContainer = mccContainer

	return kcr, nil
}

func (k *KwokDriver) startMCOContainer(ctx context.Context, result *KWOKContainerResult) (testcontainers.Container, error) {
	entrypoint := []string{
		"/usr/bin/machine-config-operator",
		"start",
		"--kubeconfig", "/etc/kubernetes/kubeconfig",
		"--images-json", "/etc/mco/images/images.json",
		"--payload-version", result.ReleaseVersion,
		"--operator-image", k.opts.ReleaseImage,
	}

	imagesJSON, err := result.ImagesJSON()
	if err != nil {
		return nil, err
	}

	containerfiles := []testcontainers.ContainerFile{
		{
			ContainerFilePath: "/etc/kubernetes/kubeconfig",
			Reader:            bytes.NewBuffer(result.ContainerKubeconfig),
		},
		{
			ContainerFilePath: "/etc/mco/images/images.json",
			Reader:            bytes.NewBuffer(imagesJSON),
		},
	}

	if k.opts.MCOBinaryPath != "" {
		containerfiles = append(containerfiles, testcontainers.ContainerFile{
			FileMode:          executableFileMode(),
			HostFilePath:      k.opts.MCOBinaryPath,
			ContainerFilePath: "/usr/bin/machine-config-operator",
		})
	}

	return testcontainers.Run(
		ctx,
		result.LookupComponentImage("machine-config-operator").From.Name,
		network.WithNetwork([]string{}, k.network),
		testcontainers.WithFiles(),
		testcontainers.WithEntrypoint(entrypoint...),
		testcontainers.WithLogConsumers(k.newLogConsumers("machine-config-operator")...),
		testcontainers.WithFiles(containerfiles...),
		testcontainers.WithProvider(testcontainers.ProviderPodman),
	)
}

func (k *KwokDriver) startMCCContainer(ctx context.Context, result *KWOKContainerResult) (testcontainers.Container, error) {
	entrypoint := []string{
		"/usr/bin/machine-config-controller",
		"start",
		"--kubeconfig", "/etc/kubernetes/kubeconfig",
		"--resourcelock-namespace", "openshift-machine-config-operator",
		"--payload-version", result.ReleaseVersion,
	}

	containerfiles := []testcontainers.ContainerFile{
		{
			ContainerFilePath: "/etc/kubernetes/kubeconfig",
			Reader:            bytes.NewBuffer(result.ContainerKubeconfig),
		},
	}

	if k.opts.MCCBinaryPath != "" {
		containerfiles = append(containerfiles, testcontainers.ContainerFile{
			FileMode:          executableFileMode(),
			HostFilePath:      k.opts.MCCBinaryPath,
			ContainerFilePath: "/usr/bin/machine-config-controller",
		})
	}

	return testcontainers.Run(
		ctx,
		result.LookupComponentImage("machine-config-operator").From.Name,
		network.WithNetwork([]string{}, k.network),
		testcontainers.WithFiles(),
		testcontainers.WithEntrypoint(entrypoint...),
		testcontainers.WithLabels(map[string]string{
			"testcontainers": "true",
		}),
		testcontainers.WithLogConsumers(k.newLogConsumers("machine-config-controller")...),
		testcontainers.WithFiles(containerfiles...),
		testcontainers.WithProvider(testcontainers.ProviderPodman),
	)
}

func (k *KwokDriver) getReleaseImageData(ctx context.Context, t *testing.T) (*imagesv1.ImageStream, string, error) {
	con, err := testcontainers.Run(
		ctx,
		k.opts.ReleaseImage,
		testcontainers.WithProvider(testcontainers.ProviderPodman),
		testcontainers.WithNoStart(),
	)
	if err != nil {
		return nil, "", err
	}

	testcontainers.CleanupContainer(t, con)

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

func (k *KwokDriver) getMCOReleaseHash(ctx context.Context, t *testing.T, is *imagesv1.ImageStream) (string, error) {
	var version []byte
	var err error

	if k.opts.MCOBinaryPath != "" {
		version, err = exec.Command(k.opts.MCOBinaryPath, "version").CombinedOutput()
		if err != nil {
			return "", err
		}
	} else {
		version, err = k.getMCOReleaseHashFromContainer(ctx, t, is)
		if err != nil {
			return "", err
		}
	}

	re := regexp.MustCompile(`(?m)[a-f0-9]{40}$`)

	match := re.FindString(string(version))
	if match == "" {
		return "", fmt.Errorf("could not get machine-config-operator version hash from %q", string(version))
	}

	return strings.TrimSpace(match), nil
}

func (k *KwokDriver) getMCOReleaseHashFromContainer(ctx context.Context, t *testing.T, is *imagesv1.ImageStream) ([]byte, error) {
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

	testcontainers.CleanupContainer(t, mcoContainer)

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

func (k *KwokDriver) decodeJSONFromContainer(ctx context.Context, con testcontainers.Container, out interface{}, path string) error {
	fileRC, err := con.CopyFileFromContainer(ctx, path)
	if err != nil {
		return err
	}

	defer fileRC.Close()

	return json.NewDecoder(fileRC).Decode(out)
}
