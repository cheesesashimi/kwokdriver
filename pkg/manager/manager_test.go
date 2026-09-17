package manager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cheesesashimi/kwokdriver/pkg/api"
	"github.com/cheesesashimi/kwokdriver/pkg/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager(t *testing.T) {
	require.NoError(t, testhelpers.PrepareForTesting())

	ctx := context.Background()

	m := NewLifecycleManager()

	require.NoError(t, m.SweepOrphans(ctx))

	t.Cleanup(func() {
		m.CleanupAll(ctx)
	})

	for i := 0; i <= 5; i++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()

			env, err := m.Provision(ctx, &api.ProvisionOpts{
				KwokClusterImage: "localhost/kwok:latest",
				TestName:         t.Name(),
			})
			assert.NoError(t, err)
			assert.NotNil(t, env)

			t.Cleanup(func() {
				require.NoError(t, m.Destroy(ctx, env.ID))
			})

			assert.NoError(t, runKubectl(t, []byte(env.Kubeconfig)))
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
