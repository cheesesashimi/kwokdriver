package builder

import (
	"context"
	"testing"

	"github.com/cheesesashimi/kwokdriver/pkg/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKwokImageBuilder(t *testing.T) {
	require.NoError(t, testhelpers.PrepareForTesting())

	builder := NewKwokImageBuilder(KwokImageOpts{
		ReleaseImage: "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-14-041141",
		KWOKImage:    "registry.k8s.io/kwok/cluster:v0.8.0-k8s.v1.36.1",
	})

	_, err := builder.Build(context.TODO(), "localhost/something:latest")
	assert.NoError(t, err)
}
