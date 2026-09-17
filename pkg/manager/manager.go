package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cheesesashimi/kwokdriver/pkg/api"
	"github.com/cheesesashimi/kwokdriver/pkg/data"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"k8s.io/klog/v2"
)

const (
	LabelManagedBy = "testharness.managed"
	LabelEnvID     = "testharness.env_id"
)

type LifecycleManager struct {
	// Active environments mapped by ID (string -> *managedEnvironment)
	activeEnvs       sync.Map
	kwokClusterImage string
}

type managedEnvironment struct {
	environment *api.Environment
	network     *testcontainers.DockerNetwork
	containers  []testcontainers.Container
}

func NewLifecycleManager(kwokClusterImage string) *LifecycleManager {
	m := &LifecycleManager{}
	m.kwokClusterImage = kwokClusterImage
	return m
}

type containerCtx struct {
	netName           string
	net               *testcontainers.DockerNetwork
	kwokContainerName string
	mcoImagePullspec  string
	labels            map[string]string
	port              string
	buildData         *data.ImageMetadata
	api.ProvisionOpts
}

func (m *LifecycleManager) getBuildDataFromKwokContainer(ctx context.Context, con testcontainers.Container) (*data.ImageMetadata, error) {
	imRC, err := con.CopyFileFromContainer(ctx, "/root/build.json")
	if err != nil {
		return nil, err
	}

	defer imRC.Close()

	i := &data.ImageMetadata{}

	if err := json.NewDecoder(imRC).Decode(i); err != nil {
		return nil, err
	}

	return i, nil
}

func (m *LifecycleManager) startKwokContainer(ctx context.Context, cctx *containerCtx) (testcontainers.Container, *data.ImageMetadata, error) {
	readyMsg := "Cluster is ready"
	klog.V(1).InfoS("Starting KWOK container", "image", cctx.KwokClusterImage)

	kwokContainer, err := m.newContainerWithOpts(ctx, cctx, cctx.KwokClusterImage, testcontainers.WithNoStart())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create KWOK container: %w", err)
	}

	bd, err := m.getBuildDataFromKwokContainer(ctx, kwokContainer)
	if err != nil {
		klog.ErrorS(err, "Failed to read build data from KWOK container")
		return nil, nil, err
	}

	if err := kwokContainer.Terminate(ctx); err != nil {
		return nil, nil, err
	}

	kwokContainer, err = m.newContainerWithOpts(ctx, cctx, cctx.KwokClusterImage,
		testcontainers.WithWaitStrategy(
			wait.ForLog(readyMsg),
			wait.ForExposedPort(),
		),
		testcontainers.WithExposedPorts(cctx.port),
		testcontainers.WithEnv(map[string]string{
			"READY_MSG":               readyMsg,
			"RELEASE_VERSION":         bd.ReleaseVersion,
			"OPENSHIFT_RELEASE_IMAGE": bd.ReleaseImage,
		}),
	)
	if err != nil {
		klog.ErrorS(err, "Failed to start KWOK container", "image", cctx.KwokClusterImage)
		return nil, nil, fmt.Errorf("failed to start KWOK container: %w", err)
	}

	klog.V(1).InfoS("Started KWOK container")

	return kwokContainer, bd, nil
}

func (m *LifecycleManager) startMCOContainer(ctx context.Context, cctx *containerCtx) (testcontainers.Container, error) {
	klog.V(1).InfoS("Starting machine-config-operator container", "image")
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

	con, err := m.newContainerWithOpts(ctx,
		cctx,
		cctx.mcoImagePullspec,
		testcontainers.WithEntrypoint(entrypoint...),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{
				ContainerFilePath: "/etc/kubernetes/kubeconfig",
				Reader:            strings.NewReader(m.getKubeconfig(cctx.kwokContainerName, cctx.port)),
			},
			testcontainers.ContainerFile{
				ContainerFilePath: "/etc/mco/images/images.json",
				Reader:            bytes.NewBuffer(imagesJSONBytes),
			},
		),
	)
	if err != nil {
		klog.ErrorS(err, "Failed to start machine-config-operator container")
		return nil, fmt.Errorf("failed to start MCO container: %w", err)
	}
	klog.V(1).InfoS("Started machine-config-operator container")

	return con, nil
}

func (m *LifecycleManager) newContainerWithOpts(ctx context.Context, cctx *containerCtx, image string, opts ...testcontainers.ContainerCustomizer) (testcontainers.Container, error) {
	opts = append(opts, []testcontainers.ContainerCustomizer{
		network.WithNetwork([]string{cctx.netName}, cctx.net),
		testcontainers.WithLabels(cctx.labels),
	}...)

	return testcontainers.Run(ctx, image, opts...)
}

func (m *LifecycleManager) startDebugContainer(ctx context.Context, cctx *containerCtx) (testcontainers.Container, error) {
	return m.newContainerWithOpts(ctx, cctx, "quay.io/zzlotnik/toolbox:workspace-fedora-44",
		testcontainers.WithFiles(testcontainers.ContainerFile{
			ContainerFilePath: "/kubeconfig",
			Reader:            strings.NewReader(m.getKubeconfig(cctx.kwokContainerName, cctx.port)),
		}),
		testcontainers.WithEntrypoint("sleep", "infinity"),
		testcontainers.WithEnv(map[string]string{"KUBECONFIG": "/kubeconfig"}),
	)
}

func (m *LifecycleManager) startMCCContainer(ctx context.Context, cctx *containerCtx) (testcontainers.Container, error) {
	klog.V(1).InfoS("Starting machine-config-controller container", "image", cctx.mcoImagePullspec)
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
			Reader:            strings.NewReader(m.getKubeconfig(cctx.kwokContainerName, cctx.port)),
		},
	}

	con, err := m.newContainerWithOpts(ctx, cctx,
		cctx.mcoImagePullspec,
		network.WithNetwork([]string{cctx.netName}, cctx.net),
		testcontainers.WithEntrypoint(entrypoint...),
		testcontainers.WithFiles(containerfiles...),
	)
	if err != nil {
		klog.ErrorS(err, "Failed to start machine-config-controller container")
		return nil, fmt.Errorf("failed to start MCC container: %w", err)
	}
	klog.V(1).InfoS("Started machine-config-controller container")

	return con, nil
}

// Provision sets up a dedicated network, a  KWOK control plane container, an MCO and an MCC container.
func (m *LifecycleManager) Provision(ctx context.Context, opts *api.ProvisionOpts) (*api.Environment, error) {
	provisionOpts := *opts
	if provisionOpts.KwokClusterImage == "" {
		provisionOpts.KwokClusterImage = m.kwokClusterImage
	}

	envID := fmt.Sprintf("env-%s-%s", opts.TestName, uuid.New().String())
	klog.InfoS("Provisioning environment", "environmentID", envID, "testName", opts.TestName)

	labels := map[string]string{
		LabelManagedBy: "true",
		LabelEnvID:     envID,
		"TestName":     opts.TestName,
	}

	env := &api.Environment{
		ID:       envID,
		TestName: opts.TestName,
	}
	managedEnv := &managedEnvironment{environment: env}

	netName := fmt.Sprintf("th-net-%s", envID)
	net, err := network.New(ctx, network.WithDriver("bridge"), network.WithLabels(labels))
	if err != nil {
		klog.ErrorS(err, "Failed to create environment network", "environmentID", envID)
		return nil, fmt.Errorf("failed to create network: %w", err)
	}
	managedEnv.network = net

	// Ensure partial failures trigger cleanup of started resources
	defer func() {
		if err != nil {
			klog.ErrorS(err, "Provisioning environment failed", "environmentID", envID)
			_ = m.Destroy(context.Background(), envID)
		}
	}()

	cctx := &containerCtx{
		netName:       netName,
		net:           net,
		port:          "8080",
		labels:        labels,
		ProvisionOpts: provisionOpts,
	}

	kwokContainer, buildData, err := m.startKwokContainer(ctx, cctx)
	if err != nil {
		klog.ErrorS(err, "Failed to start KWOK container", "environmentID", envID)
		return nil, err
	}

	if opts.ReleaseImage != "" {
		originalReleaseImage := buildData.ReleaseImage

		buildData, err = data.NewFromReleaseImage(ctx, opts.ReleaseImage)
		if err != nil {
			return nil, err
		}

		klog.Warningf("Overriding %s with %s", originalReleaseImage, opts.ReleaseImage)

	}

	mcoImage, err := buildData.LookupComponent("machine-config-operator")
	if err != nil {
		return nil, err
	}

	klog.Warningf("Using %s for MCO image", mcoImage)

	cctx.mcoImagePullspec = mcoImage.From.Name

	inspect, err := kwokContainer.Inspect(ctx)
	if err != nil {
		return nil, err
	}

	cctx.kwokContainerName = strings.TrimPrefix(inspect.Name, "/")

	cctx.buildData = buildData

	managedEnv.containers = append(managedEnv.containers, kwokContainer)

	hostIP, err := kwokContainer.Host(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to determine KWOK container host", "environmentID", envID)
		return nil, err
	}
	mappedPort, err := kwokContainer.MappedPort(ctx, cctx.port)
	if err != nil {
		klog.ErrorS(err, "Failed to determine KWOK container port", "environmentID", envID)
		return nil, err
	}

	env.Kubeconfig = m.getKubeconfig(hostIP, mappedPort.Port())

	mcoContainer, err := m.startMCOContainer(ctx, cctx)
	if err != nil {
		return nil, err
	}

	managedEnv.containers = append(managedEnv.containers, mcoContainer)

	mccContainer, err := m.startMCCContainer(ctx, cctx)
	if err != nil {
		return nil, err
	}

	managedEnv.containers = append(managedEnv.containers, mccContainer)

	debugContainer, err := m.startDebugContainer(ctx, cctx)
	if err != nil {
		return nil, err
	}

	managedEnv.containers = append(managedEnv.containers, debugContainer)

	// Store tracked environment
	m.activeEnvs.Store(envID, managedEnv)
	klog.InfoS("Provisioned environment", "environmentID", envID, "testName", opts.TestName)
	return env, nil
}

// Destroy tears down all containers and the network associated with an environment ID.
func (m *LifecycleManager) Destroy(ctx context.Context, envID string) error {
	val, ok := m.activeEnvs.LoadAndDelete(envID)
	if !ok {
		klog.V(1).InfoS("Environment was already destroyed or does not exist", "environmentID", envID)
		return nil // Already destroyed or non-existent
	}
	env := val.(*managedEnvironment)

	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)

	// Terminate all containers concurrently, then wait for every container to
	// finish before removing the network they share.
	for _, container := range env.containers {
		if container == nil {
			continue
		}

		wg.Add(1)
		go func(container testcontainers.Container) {
			defer wg.Done()
			if err := container.Terminate(ctx); err != nil {
				klog.ErrorS(err, "Failed to terminate environment container", "environmentID", envID)
				errsMu.Lock()
				errs = append(errs, err)
				errsMu.Unlock()
			}
		}(container)
	}
	wg.Wait()

	// Remove network
	if env.network != nil {
		if err := env.network.Remove(ctx); err != nil {
			klog.ErrorS(err, "Failed to remove environment network", "environmentID", envID)
			errs = append(errs, err)
		}
	}

	err := errors.Join(errs...)
	if err != nil {
		klog.ErrorS(err, "Failed to destroy environment", "environmentID", envID)
	} else {
		klog.InfoS("Destroyed environment", "environmentID", envID)
	}
	return err
}

// CleanupAll stops all active tracked environments gracefully (called on daemon shutdown).
func (m *LifecycleManager) CleanupAll(ctx context.Context) error {
	klog.InfoS("Cleaning up active environments")
	var wg sync.WaitGroup
	var errsMu sync.Mutex
	var errs []error
	m.activeEnvs.Range(func(key, val any) bool {
		envID := key.(string)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := m.Destroy(ctx, id); err != nil {
				errsMu.Lock()
				errs = append(errs, err)
				errsMu.Unlock()
			}
		}(envID)
		return true
	})
	wg.Wait()
	err := errors.Join(errs...)
	if err != nil {
		klog.ErrorS(err, "Failed to clean up all active environments")
	} else {
		klog.InfoS("Finished cleaning up active environments")
	}
	return err
}

// SweepOrphans scans Docker for containers and networks carrying daemon labels leftover from crashed runs.
func (m *LifecycleManager) SweepOrphans(ctx context.Context) error {
	klog.InfoS("Sweeping orphaned resources")
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

	klog.InfoS("Finished sweeping orphaned resources")
	return nil
}

func (m *LifecycleManager) sweepOrphanNetworks(ctx context.Context, provider *testcontainers.DockerProvider) error {
	result, err := provider.Client().NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return err
	}

	for _, n := range result.Items {
		if _, ok := n.Labels[LabelManagedBy]; ok || n.Name == "reaper_default" {
			klog.V(1).InfoS("Removing orphaned network", "network", n.Name)
			if _, err := provider.Client().NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
				klog.ErrorS(err, "Failed to remove orphaned network", "network", n.Name)
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
			klog.V(1).InfoS("Removing orphaned container", "containerID", c.ID)
			_, err := provider.Client().ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{
				Force: true,
			})
			if err != nil {
				klog.ErrorS(err, "Failed to remove orphaned container", "containerID", c.ID)
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
