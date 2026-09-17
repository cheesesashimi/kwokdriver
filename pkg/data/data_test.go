package data

import (
	"context"
	"testing"

	"github.com/cheesesashimi/kwokdriver/pkg/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector(t *testing.T) {
	require.NoError(t, testhelpers.PrepareForTesting())
	releaseImage := "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-15-234048"
	data, err := NewFromReleaseImage(context.TODO(), releaseImage)
	assert.NoError(t, err)
	assert.NotNil(t, data)
}

func TestCollectorFromKwokImage(t *testing.T) {
	require.NoError(t, testhelpers.PrepareForTesting())
	data, err := NewFromKWOKClusterImage(context.TODO(), "localhost/kwok:latest")
	assert.NoError(t, err)
	assert.NotNil(t, data)
}
