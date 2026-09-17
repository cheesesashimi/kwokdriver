package data

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	imagesv1 "github.com/openshift/api/image/v1"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type collector struct {
	container testcontainers.Container
	image     string
}

func newCollector(ctx context.Context, image string) (*collector, error) {
	con, err := testcontainers.Run(
		ctx,
		image,
		testcontainers.WithNoStart(),
	)
	if err != nil {
		return nil, err
	}

	return &collector{container: con, image: image}, nil
}

func (c *collector) getMCOReleaseHash(ctx context.Context, is *imagesv1.ImageStream) (string, error) {
	version, err := c.getMCOReleaseHashFromContainer(ctx, is)
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

func (c *collector) getMCOReleaseHashFromContainer(ctx context.Context, is *imagesv1.ImageStream) ([]byte, error) {
	i := &ImageMetadata{Imagestream: is, ReleaseImage: c.image}

	mcoImage, err := i.LookupComponent("machine-config-operator")
	if err != nil {
		return nil, err
	}

	mcoContainer, err := testcontainers.Run(
		ctx,
		mcoImage.From.Name,
		testcontainers.WithEntrypoint("/usr/bin/machine-config-operator", "version"),
		testcontainers.WithWaitStrategy(wait.ForExit()),
	)
	if err != nil {
		return nil, err
	}

	defer mcoContainer.Terminate(ctx)

	reader, err := mcoContainer.Logs(ctx)
	if err != nil {
		return nil, err
	}

	defer reader.Close()

	return io.ReadAll(reader)
}

func (c *collector) decodeJSONFromContainer(ctx context.Context, out interface{}, path string) error {
	fileRC, err := c.container.CopyFileFromContainer(ctx, path)
	if err != nil {
		return err
	}

	defer fileRC.Close()

	return json.NewDecoder(fileRC).Decode(out)
}

func getImagesFromImageStream(is *imagesv1.ImageStream, relVersion string) (map[string]string, error) {
	images := map[string]string{
		"baremetalRuntimeCfgImage": "baremetal-runtimecfg",
		"corednsImage":             "coredns",
		"dockerRegistryImage":      "docker-registry",
		"haproxyImage":             "haproxy-router",
		"infraImage":               "pod",
		"keepalivedImage":          "keepalived-ipfailover",
		"kubeRbacProxy":            "kube-rbac-proxy",
		"machineConfigOperator":    "machine-config-operator",
		"oauthProxy":               "oauth-proxy",
	}

	for key, componentName := range images {
		tagRef := lookupComponentFromImageStream(is, componentName)
		if tagRef == nil {
			return nil, fmt.Errorf("no image found for component %q", componentName)
		}

		images[key] = tagRef.From.Name
	}

	images["releaseVersion"] = relVersion

	return images, nil
}

func lookupComponentFromImageStream(is *imagesv1.ImageStream, name string) *imagesv1.TagReference {
	for _, tag := range is.Spec.Tags {
		if tag.Name == name {
			return tag.DeepCopy()
		}
	}

	return nil
}
