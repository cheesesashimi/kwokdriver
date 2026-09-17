package kwokdriver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	m := NewLifecycleManager()

	require.NoError(t, m.SweepOrphans(ctx))

	t.Cleanup(func() {
		m.CleanupAll(ctx)
	})

	for i := 0; i <= 5; i++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()

			env, err := m.Provision(ctx, &ProvisionOpts{
				KwokClusterImage: "localhost/kwok:latest",
				ReleaseImage:     "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-14-041141",
				TestName:         t.Name(),
			})
			assert.NoError(t, err)
			assert.NotNil(t, env)

			t.Cleanup(func() {
				require.NoError(t, m.Destroy(ctx, env.ID))
			})

			kcfg, err := newKubeconfigs(ctx, env.Containers[0], env.Network)
			require.NoError(t, err)

			t.Run("kubeconfig-lib", func(t *testing.T) {
				assert.NoError(t, runKubectl(t, kcfg.HostKubeconfig))
			})

			t.Run("generated", func(t *testing.T) {
				assert.NoError(t, runKubectl(t, []byte(env.Kubeconfig)))
			})
		})
	}
}

func runKubectl(t *testing.T, kubecfg []byte) error {
	t.Log(string(kubecfg))
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, kubecfg, 0o755); err != nil {
		return err
	}

	cmd := exec.Command("kubectl", "get", "machineconfigs")
	cmd.Env = []string{"KUBECONFIG=" + path}
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	return err
}
