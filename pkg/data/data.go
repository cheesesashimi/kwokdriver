package data

import (
	"context"
	"fmt"

	imagesv1 "github.com/openshift/api/image/v1"
)

type ImageMetadata struct {
	Imagestream    *imagesv1.ImageStream `json:"imagestream,omitempty"`
	ReleaseImage   string                `json:"releaseImage,omitempty"`
	ReleaseVersion string                `json:"releaseVersion,omitempty"`
	McoReleaseHash string                `json:"mcoReleaseHash,omitempty"`
	Images         map[string]string     `json:"images"`
}

func (i *ImageMetadata) LookupComponent(name string) (*imagesv1.TagReference, error) {
	ref := lookupComponentFromImageStream(i.Imagestream, name)
	if ref == nil {
		return nil, fmt.Errorf("could not find image for component %q in release image %s", name, i.ReleaseImage)
	}

	if ref.From.Name == "" {
		return nil, fmt.Errorf("found image ref for %q but pullspec empty for %s", name, i.ReleaseImage)
	}

	return ref, nil
}

func NewFromReleaseImage(ctx context.Context, pullspec string) (*ImageMetadata, error) {
	c, err := newCollector(ctx, pullspec)
	if err != nil {
		return nil, err
	}

	defer c.container.Terminate(ctx)

	is := &imagesv1.ImageStream{}

	if err := c.decodeJSONFromContainer(ctx, is, "/release-manifests/image-references"); err != nil {
		return nil, err
	}

	type relVersion struct {
		Version string `json:"version"`
	}

	rv := &relVersion{}

	if err := c.decodeJSONFromContainer(ctx, rv, "/release-manifests/release-metadata"); err != nil {
		return nil, err
	}

	mcoReleaseHash, err := c.getMCOReleaseHash(ctx, is)
	if err != nil {
		return nil, fmt.Errorf("could not get mco release hash: %w", err)
	}

	images, err := getImagesFromImageStream(is, rv.Version)
	if err != nil {
		return nil, fmt.Errorf("could not get images from imagestream: %w", err)
	}

	bd := &ImageMetadata{
		Imagestream:    is,
		ReleaseImage:   pullspec,
		ReleaseVersion: rv.Version,
		McoReleaseHash: mcoReleaseHash,
		Images:         images,
	}

	return bd, nil
}

func NewFromKWOKClusterImage(ctx context.Context, pullspec string) (*ImageMetadata, error) {
	c, err := newCollector(ctx, pullspec)
	if err != nil {
		return nil, err
	}

	defer c.container.Terminate(ctx)

	i := &ImageMetadata{}

	if err := c.decodeJSONFromContainer(ctx, i, "/root/build.json"); err != nil {
		return nil, err
	}

	return i, nil
}
