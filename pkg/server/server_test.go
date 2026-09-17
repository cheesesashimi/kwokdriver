package server

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cheesesashimi/kwokdriver/pkg/api"
	"github.com/cheesesashimi/kwokdriver/pkg/client"
	"github.com/stretchr/testify/require"
)

type fakeManager struct {
	provision    func(context.Context, *api.ProvisionOpts) (*api.Environment, error)
	destroy      func(context.Context, string) error
	cleanupAll   func(context.Context) error
	sweepOrphans func(context.Context) error
}

func (m *fakeManager) Provision(ctx context.Context, opts *api.ProvisionOpts) (*api.Environment, error) {
	return m.provision(ctx, opts)
}

func (m *fakeManager) Destroy(ctx context.Context, id string) error {
	return m.destroy(ctx, id)
}

func (m *fakeManager) CleanupAll(ctx context.Context) error {
	return m.cleanupAll(ctx)
}

func (m *fakeManager) SweepOrphans(ctx context.Context) error {
	return m.sweepOrphans(ctx)
}

func TestClientServer(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	manager := &fakeManager{
		provision: func(ctx context.Context, opts *api.ProvisionOpts) (*api.Environment, error) {
			require.Equal(t, "test", opts.TestName)
			mu.Lock()
			calls = append(calls, "provision")
			mu.Unlock()
			return &api.Environment{ID: "env-1", TestName: opts.TestName}, nil
		},
		destroy: func(ctx context.Context, id string) error {
			require.Equal(t, "env-1", id)
			mu.Lock()
			calls = append(calls, "destroy")
			mu.Unlock()
			return nil
		},
		cleanupAll: func(context.Context) error {
			mu.Lock()
			calls = append(calls, "cleanup")
			mu.Unlock()
			return nil
		},
		sweepOrphans: func(context.Context) error {
			mu.Lock()
			calls = append(calls, "sweep")
			mu.Unlock()
			return nil
		},
	}

	server, client, stop := startTestServer(t, manager)
	defer stop()

	env, err := client.Provision(context.Background(), &api.ProvisionOpts{TestName: "test"})
	require.NoError(t, err)
	require.Equal(t, "env-1", env.ID)
	require.NoError(t, client.Destroy(context.Background(), env.ID))
	require.NoError(t, client.CleanupAll(context.Background()))
	require.NoError(t, client.SweepOrphans(context.Background()))
	require.NotNil(t, server)

	mu.Lock()
	require.Equal(t, []string{"sweep", "provision", "destroy", "cleanup", "sweep"}, calls)
	mu.Unlock()
}

func TestClientCancellation(t *testing.T) {
	started := make(chan struct{})
	manager := &fakeManager{
		provision: func(ctx context.Context, _ *api.ProvisionOpts) (*api.Environment, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		cleanupAll:   func(context.Context) error { return nil },
		destroy:      func(context.Context, string) error { return nil },
		sweepOrphans: func(context.Context) error { return nil },
	}
	_, client, stop := startTestServer(t, manager)
	defer stop()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Provision(ctx, &api.ProvisionOpts{})
		result <- err
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("client request did not cancel")
	}
}

func TestServerShutdown(t *testing.T) {
	cleanupDone := make(chan struct{})
	manager := &fakeManager{
		provision: func(context.Context, *api.ProvisionOpts) (*api.Environment, error) {
			return nil, errors.New("not used")
		},
		destroy: func(context.Context, string) error { return nil },
		cleanupAll: func(ctx context.Context) error {
			close(cleanupDone)
			return nil
		},
		sweepOrphans: func(context.Context) error { return nil },
	}

	_, _, stop := startTestServer(t, manager)
	stop()
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("server did not clean up on shutdown")
	}
}

func startTestServer(t *testing.T, manager Manager) (*Server, *client.Client, func()) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "manager.sock")
	server := NewServer(manager, socketPath)
	ctx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Start(ctx) }()

	deadline := time.Now().Add(time.Second)
	for {
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", socketPath)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	client := client.NewClient(socketPath)
	stop := func() {
		cancel()
		select {
		case err := <-serverErr:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
		_, err := os.Stat(socketPath)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	return server, client, stop
}
