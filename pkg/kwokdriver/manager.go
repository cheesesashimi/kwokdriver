package kwokdriver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	LabelManagedBy = "testharness.managed"
	LabelEnvID     = "testharness.env_id"
	//	KwokImage      = "kwok-openshift-mco:latest" // Replace with your built image reference
	KwokImage      = "localhost/kwok:latest"
	SatelliteImage = "alpine:latest" // Replace with your satellite container image
)

// Environment holds the Docker resources for a single isolated test run.
type Environment struct {
	ID         string                        `json:"environment_id"`
	TestName   string                        `json:"test_name"`
	Kubeconfig string                        `json:"kubeconfig"`
	Network    *testcontainers.DockerNetwork `json:"-"`
	Containers []testcontainers.Container    `json:"-"`
}

type ImageOrPath struct {
	Pullspec string
	Path     string
}

type ProvisionOpts struct {
	TestName         string
	ReleaseImage     string
	MCO              ImageOrPath
	MCC              ImageOrPath
	KwokClusterImage string
}

type LifecycleManager struct {
	// Active environments mapped by ID (string -> *Environment)
	activeEnvs sync.Map
}

func NewLifecycleManager() *LifecycleManager {
	return &LifecycleManager{}
}

type containerCtx struct {
	netName   string
	net       *testcontainers.DockerNetwork
	labels    map[string]string
	port      string
	buildData *BuildData
	ProvisionOpts
}

func (m *LifecycleManager) getBuildDataFromKwokContainer(ctx context.Context, con testcontainers.Container) (*BuildData, error) {
	bdRC, err := con.CopyFileFromContainer(ctx, "/root/build.json")
	if err != nil {
		return nil, err
	}

	defer bdRC.Close()

	bd := &BuildData{}

	if err := json.NewDecoder(bdRC).Decode(bd); err != nil {
		return nil, err
	}

	return bd, nil
}

func (m *LifecycleManager) startKwokContainer(ctx context.Context, cctx *containerCtx) (testcontainers.Container, *BuildData, error) {
	readyMsg := "Cluster is ready"

	// 2. Provision KWOK Container
	kwokContainer, err := testcontainers.Run(
		ctx,
		cctx.KwokClusterImage,
		testcontainers.WithWaitStrategy(
			wait.ForLog(readyMsg),
			wait.ForExposedPort(),
		),
		testcontainers.WithExposedPorts(cctx.port),
		testcontainers.WithEnv(map[string]string{
			"READY_MSG": readyMsg,
		}),
		network.WithNetwork([]string{cctx.netName}, cctx.net),
		testcontainers.WithLabels(cctx.labels),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start KWOK container: %w", err)
	}

	bd, err := m.getBuildDataFromKwokContainer(ctx, kwokContainer)
	if err != nil {
		return nil, nil, err
	}

	return kwokContainer, bd, nil
}

func (m *LifecycleManager) startMCOContainer(ctx context.Context, cctx *containerCtx) (testcontainers.Container, error) {
	entrypoint := []string{
		"/usr/bin/machine-config-operator",
		"start",
		"--kubeconfig", "/etc/kubernetes/kubeconfig",
		"--images-json", "/etc/mco/images/images.json",
		"--payload-version", cctx.buildData.ReleaseVersion,
		"--operator-image", cctx.buildData.ReleaseImage,
	}

	imagesJSONBytes, err := json.Marshal(cctx.buildData.Images)
	if err != nil {
		return nil, err
	}

	containerfiles := []testcontainers.ContainerFile{
		{
			ContainerFilePath: "/etc/kubernetes/kubeconfig",
			Reader:            strings.NewReader(m.getKubeconfig("localhost", cctx.port)),
		},
		{
			ContainerFilePath: "/etc/mco/images/images.json",
			Reader:            bytes.NewBuffer(imagesJSONBytes),
		},
	}

	con, err := testcontainers.Run(
		ctx,
		lookupComponentFromImageStream(cctx.buildData.Imagestream, "machine-config-operator").From.Name,
		network.WithNetwork([]string{cctx.netName}, cctx.net),
		testcontainers.WithEntrypoint(entrypoint...),
		testcontainers.WithFiles(containerfiles...),
		testcontainers.WithLabels(cctx.labels),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start MCO container: %w", err)
	}

	return con, nil
}

func (m *LifecycleManager) startMCCContainer(ctx context.Context, cctx *containerCtx) (testcontainers.Container, error) {
	entrypoint := []string{
		"/usr/bin/machine-config-controller",
		"start",
		"--kubeconfig", "/etc/kubernetes/kubeconfig",
		"--resourcelock-namespace", "openshift-machine-config-operator",
		"--payload-version", cctx.buildData.ReleaseVersion,
	}

	containerfiles := []testcontainers.ContainerFile{
		{
			ContainerFilePath: "/etc/kubernetes/kubeconfig",
			Reader:            strings.NewReader(m.getKubeconfig("localhost", cctx.port)),
		},
	}

	con, err := testcontainers.Run(
		ctx,
		lookupComponentFromImageStream(cctx.buildData.Imagestream, "machine-config-operator").From.Name,
		network.WithNetwork([]string{cctx.netName}, cctx.net),
		testcontainers.WithEntrypoint(entrypoint...),
		testcontainers.WithLabels(cctx.labels),
		testcontainers.WithFiles(containerfiles...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start MCC container: %w", err)
	}

	return con, nil
}

// Provision sets up a dedicated network, a KWOK control plane container, and two satellite containers.
func (m *LifecycleManager) Provision(ctx context.Context, opts *ProvisionOpts) (*Environment, error) {
	envID := fmt.Sprintf("env-%s", uuid.New().NodeID())

	labels := map[string]string{
		LabelManagedBy: "true",
		LabelEnvID:     envID,
		"TestName":     opts.TestName,
	}

	env := &Environment{
		ID:         envID,
		TestName:   opts.TestName,
		Containers: []testcontainers.Container{},
	}

	// 1. Create isolated Docker bridge network per test
	netName := fmt.Sprintf("th-net-%s", envID)
	net, err := network.New(ctx, network.WithDriver("bridge"), network.WithLabels(labels))
	if err != nil {
		return nil, fmt.Errorf("failed to create network: %w", err)
	}
	env.Network = net

	// Ensure partial failures trigger cleanup of started resources
	defer func() {
		if err != nil {
			_ = m.Destroy(context.Background(), envID)
		}
	}()

	cctx := &containerCtx{
		netName:       netName,
		net:           net,
		port:          "8080",
		labels:        labels,
		ProvisionOpts: *opts,
	}

	kwokContainer, buildData, err := m.startKwokContainer(ctx, cctx)
	if err != nil {
		return nil, err
	}

	cctx.buildData = buildData

	env.Containers = append(env.Containers, kwokContainer)

	// Extract dynamic host/port and fetch Kubeconfig from container
	hostIP, err := kwokContainer.Host(ctx)
	if err != nil {
		return nil, err
	}
	mappedPort, err := kwokContainer.MappedPort(ctx, cctx.port)
	if err != nil {
		return nil, err
	}

	env.Kubeconfig = m.getKubeconfig(hostIP, mappedPort.Port())

	mcoContainer, err := m.startMCOContainer(ctx, cctx)
	if err != nil {
		return nil, err
	}

	env.Containers = append(env.Containers, mcoContainer)

	mccContainer, err := m.startMCCContainer(ctx, cctx)
	if err != nil {
		return nil, err
	}

	env.Containers = append(env.Containers, mccContainer)

	// Store tracked environment
	m.activeEnvs.Store(envID, env)
	return env, nil
}

// Destroy tears down all containers and the network associated with an environment ID.
func (m *LifecycleManager) Destroy(ctx context.Context, envID string) error {
	val, ok := m.activeEnvs.LoadAndDelete(envID)
	if !ok {
		return nil // Already destroyed or non-existent
	}
	env := val.(*Environment)

	errs := []error{}

	// Stop and remove all containers in reverse creation order
	// (Dunno why Gemini did this...)
	for i := len(env.Containers) - 1; i >= 0; i-- {
		if env.Containers[i] != nil {
			if err := env.Containers[i].Terminate(ctx); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// Remove network
	if env.Network != nil {
		if err := env.Network.Remove(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// CleanupAll stops all active tracked environments gracefully (called on daemon shutdown).
func (m *LifecycleManager) CleanupAll(ctx context.Context) {
	var wg sync.WaitGroup
	m.activeEnvs.Range(func(key, val interface{}) bool {
		envID := key.(string)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = m.Destroy(ctx, id)
		}(envID)
		return true
	})
	wg.Wait()
}

// SweepOrphans scans Docker for containers and networks carrying daemon labels leftover from crashed runs.
func (m *LifecycleManager) SweepOrphans(ctx context.Context) error {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return err
	}
	defer provider.Close()

	if err := m.sweepOrphanContainers(ctx, provider); err != nil {
		return err
	}

	if err := m.sweepOrphanNetworks(ctx, provider); err != nil {
		return err
	}

	return nil
}

func (m *LifecycleManager) sweepOrphanNetworks(ctx context.Context, provider *testcontainers.DockerProvider) error {
	result, err := provider.Client().NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return err
	}

	for _, n := range result.Items {
		if _, ok := n.Labels[LabelManagedBy]; ok || n.Name == "reaper_default" {
			if _, err := provider.Client().NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
				return err
			}
		}
	}

	return nil
}

func (m *LifecycleManager) sweepOrphanContainers(ctx context.Context, provider *testcontainers.DockerProvider) error {
	result, err := provider.Client().ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list orphan containers: %w", err)
	}

	for _, c := range result.Items {
		if _, ok := c.Labels[LabelManagedBy]; ok {
			_, err := provider.Client().ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{
				Force: true,
			})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// Reads the raw kubeconfig file from the KWOK container and updates its target URL to point to localhost.
func (m *LifecycleManager) getKubeconfig(host string, port string) string {
	return fmt.Sprintf(`
apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: http://%s:%s
  name: kwok-cluster
contexts:
- context:
    cluster: kwok-cluster
    user: kwok-admin
  name: kwok-context
current-context: kwok-context
kind: Config
preferences: {}
users:
- name: kwok-admin
  user:
    token: stub-token
`, host, port)
}
