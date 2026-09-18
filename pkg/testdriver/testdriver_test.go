package testdriver

import (
	"context"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var driver *TestDriver

func TestMain(m *testing.M) {
	exitCode, err := StartTestDriver(m)
	if err != nil {
		log.Fatalln(err)
	}

	os.Exit(exitCode)
}

func StartTestDriver(m *testing.M) (int, error) {
	driver = NewTestDriver(NewConfigFromEnv())

	if err := driver.Start(); err != nil {
		return 1, err
	}

	exitCode := m.Run()

	return exitCode, driver.Stop()
}

func TestDriverMachineConfigs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cfg := driver.Config()
	cfg.WithDebugContainer = true

	env, err := driver.NewTestEnvironment(ctx, t, cfg)
	require.NoError(t, err)

	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfigPath, []byte(env.Kubeconfig), 0o755))

	found := false

	for {
		cmd := exec.Command("kubectl", "get", "machineconfigs")
		cmd.Env = []string{"KUBECONFIG=" + kubeconfigPath}
		output, err := cmd.CombinedOutput()
		if strings.Contains(string(output), "rendered-") {
			found = true
			t.Logf("Rendered MachineConfigs found!")
			break
		}

		require.NoError(t, err)
		// Don't hammer the API server.
		time.Sleep(time.Second * time.Duration(rand.IntN(5-1+1)))
	}

	assert.True(t, found)
}

func TestDriverNodes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	env, err := driver.NewDefaultTestEnvironment(ctx, t)
	require.NoError(t, err)

	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfigPath, []byte(env.Kubeconfig), 0o755))

	found := false

	for {
		cmd := exec.Command("kubectl", "get", "nodes")
		cmd.Env = []string{"KUBECONFIG=" + kubeconfigPath}
		output, err := cmd.CombinedOutput()
		if strings.Contains(string(output), "Ready") {
			found = true
			t.Logf("Nodes found!")
			break
		}

		require.NoError(t, err)
		// Don't hammer the API server.
		time.Sleep(time.Second * time.Duration(rand.IntN(5-1+1)))
	}

	assert.True(t, found)
}
