package testdriver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/cheesesashimi/kwokdriver/pkg/api"
	"github.com/cheesesashimi/kwokdriver/pkg/client"
	"k8s.io/klog"
)

type Config struct {
	KwokClusterImage   string
	ReleaseImage       string
	KwokImage          string
	KwokDriverPath     string
	KwokDriverSocket   string
	WithDebugContainer bool
}

func (c *Config) getKwokDriverPath() (string, error) {
	path, err := exec.LookPath("kwokdriver")
	// If we found it in PATH and a path was not provided to us, use the value
	// from PATH.
	if err == nil && c.KwokDriverPath == "" {
		return path, nil
	}

	// If the error was anything but a not found, return it.
	if !errors.Is(err, exec.ErrNotFound) {
		return "", err
	}

	// If the path was provided and it exists, use it.
	if c.KwokDriverPath != "" {
		if _, err := os.Stat(c.KwokDriverPath); err != nil {
			return "", err
		}

		return c.KwokDriverPath, nil
	}

	return "", fmt.Errorf("no kwokdriver found in PATH or at KWOKDRIVER_BINARY_PATH")
}

func NewConfigFromEnv() Config {
	return Config{
		KwokClusterImage: getEnvVarOrDefault("KWOK_CLUSTER_IMAGE", "localhost/kwok:latest"),
		KwokImage:        getEnvVarOrDefault("KWOK_IMAGE", "registry.k8s.io/kwok/cluster:v0.8.0-k8s.v1.36.1"),
		ReleaseImage:     getEnvVarOrDefault("RELEASE_IMAGE", "quay-proxy.ci.openshift.org/openshift/ci:rc_payload__5.0.0-0.ci-2026-09-15-234048"),
		KwokDriverSocket: getEnvVarOrDefault("KWOKDRIVER_SOCKET", "/tmp/kwokdriver.sock"),
		KwokDriverPath:   getEnvVarOrDefault("KWOKDRIVER_BINARY_PATH", ""),
	}
}

type TestDriver struct {
	cfg      Config
	client   *client.Client
	stopFunc func() error
}

func NewTestDriver(cfg Config) *TestDriver {
	return &TestDriver{cfg: cfg, client: client.NewClient(cfg.KwokDriverSocket)}
}

func (d *TestDriver) Config() Config {
	return d.cfg
}

func (d *TestDriver) Start() error {
	if d.stopFunc != nil {
		return fmt.Errorf("testdriver may already be started")
	}

	if err := d.buildClusterImage(); err != nil {
		return err
	}

	stopFunc, err := d.startKwokDriver()
	if err != nil {
		return err
	}

	d.stopFunc = stopFunc

	return nil
}

func (d *TestDriver) Stop() error {
	if err := d.client.CleanupAll(context.TODO()); err != nil {
		return err
	}

	if err := d.stopFunc(); err != nil {
		return err
	}

	d.stopFunc = nil
	return nil
}

func (d *TestDriver) NewDefaultTestEnvironment(ctx context.Context, t *testing.T) (*api.Environment, error) {
	return d.NewTestEnvironment(ctx, t, d.cfg)
}

func (d *TestDriver) NewTestEnvironment(ctx context.Context, t *testing.T, cfg Config) (*api.Environment, error) {
	env, err := d.client.Provision(ctx, &api.ProvisionOpts{
		ReleaseImage:     cfg.ReleaseImage,
		KwokClusterImage: cfg.KwokClusterImage,
		TestName:         t.Name(),
	})
	if err == nil {
		t.Cleanup(func() {
			if err := d.client.Destroy(ctx, env.ID); err != nil {
				t.Log(err)
			}
		})
	}

	return env, err
}

func (d *TestDriver) buildClusterImage() error {
	path, err := d.cfg.getKwokDriverPath()
	if err != nil {
		return err
	}

	cmd := exec.Command(path, "build", "--release-image", d.cfg.ReleaseImage, "--kwok-image", d.cfg.KwokImage, "--final-image", d.cfg.KwokClusterImage)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not build kwok cluster image: %w", err)
	}
	return nil
}

func (d *TestDriver) startKwokDriver() (func() error, error) {
	emptyFunc := func() error { return nil }

	path, err := d.cfg.getKwokDriverPath()
	if err != nil {
		return emptyFunc, err
	}

	cmd := exec.Command(path, "start", "--kwok-cluster-image", d.cfg.KwokClusterImage)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return emptyFunc, err
	}

	klog.Info("kwokdriver started with PID:", cmd.Process.Pid)

	isCalled := false
	var killErr error

	stopFunc := func() error {
		if isCalled {
			return killErr
		}
		isCalled = true
		killErr = cmd.Process.Signal(syscall.SIGTERM)
		return killErr
	}

	for {
		isReady, err := checkSocketReady(d.cfg.KwokDriverSocket)
		if err != nil {
			return emptyFunc, err
		}

		if isReady {
			break
		}

		time.Sleep(time.Millisecond * 10)
	}

	return stopFunc, nil
}

func checkSocketReady(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}

	conn, err := net.DialTimeout("unix", path, 10*time.Millisecond)
	if err == nil {
		if cErr := conn.Close(); cErr != nil {
			return false, cErr
		}

		return true, nil
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return false, nil
	}

	return false, err
}

func getEnvVarOrDefault(envVarName, defaultVal string) string {
	envVarVal, od := os.LookupEnv(envVarName)
	if !od || envVarVal == "" {
		return defaultVal
	}

	return envVarVal
}
