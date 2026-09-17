package kwokdriver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKwokImageBuilder(t *testing.T) {
	builder := NewKwokImageBuilder(KwokImageOpts{
		ReleaseImage: "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-14-041141",
		KWOKImage:    "registry.k8s.io/kwok/cluster:v0.8.0-k8s.v1.36.1",
	})

	_, err := builder.Build(context.TODO(), "localhost/kwok:latest")
	assert.NoError(t, err)
}

func TestKwokImageBuilderGetData(t *testing.T) {
	builder := NewKwokImageBuilder(KwokImageOpts{
		ReleaseImage: "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-14-041141",
		KWOKImage:    "registry.k8s.io/kwok/cluster:v0.8.0-k8s.v1.36.1",
	})

	_, err := builder.GetBuildData(context.TODO())
	assert.NoError(t, err)
}
