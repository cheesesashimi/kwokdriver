package kwokdriver

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/user"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if err := prepareForTesting(); err != nil {
		fmt.Println("could not prepare for testing:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func prepareForTesting() error {
	if _, exists := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !exists {
		os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
		fmt.Println("Disabled ryuk")
	}

	if _, exists := os.LookupEnv("DOCKER_HOST"); !exists {
		u, err := user.Current()
		if err != nil {
			return err
		}

		h := fmt.Sprintf("unix:///run/user/%s/podman/podman.sock", u.Uid)
		os.Setenv("DOCKER_HOST", h)
		fmt.Println("Set DOCKER_HOST=" + h)
	}

	// 	if registryAuthFile, exists := os.LookupEnv("REGISTRY_AUTH_FILE"); exists && registryAuthFile != "" {
	// 		content, err := os.ReadFile(registryAuthFile)
	// 		if err != nil {
	// 			return err
	// 		}
	//
	// 		if len(content) == 0 {
	// 			return fmt.Errorf("expected %s not to be empty", registryAuthFile)
	// 		}
	//
	// 		os.Setenv("DOCKER_AUTH_CONFIG", string(content))
	// 	}

	return nil
}

func TestKwokDriver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	releaseImage, exists := os.LookupEnv("OPENSHIFT_RELEASE_IMAGE")
	if !exists {
		// releaseImage = "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-08-180714"
		releaseImage = "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-14-041141"
	}

	kid := NewKwokDriver(Opts{
		ReleaseImage: releaseImage,
		KWOKImage:    "registry.k8s.io/kwok/cluster:v0.8.0-k8s.v1.36.1",
		//		KWOKClusterImage: "localhost/kwok:latest",
	})

	result, err := kid.SetupKWOKImage(ctx, t)
	require.NoError(t, err)

	t.Cleanup(func() {
		timeout := time.Duration(0)
		require.NoError(t, result.KWOKContainer.Stop(ctx, &timeout))
		require.NoError(t, result.MCOContainer.Stop(ctx, &timeout))
		require.NoError(t, result.MCCContainer.Stop(ctx, &timeout))
		require.NoError(t, result.Network.Remove(ctx))
		time.Sleep(time.Second)
	})

	assert.NoError(t, runKubectl(t, result.HostKubeconfig))
}
