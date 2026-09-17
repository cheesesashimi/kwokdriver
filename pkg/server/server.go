package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/cheesesashimi/kwokdriver/pkg/api"
	"k8s.io/klog/v2"
)

const (
	provisionPath    = "/api/v1/environments"
	cleanupPath      = "/api/v1/cleanup"
	sweepOrphansPath = "/api/v1/sweep-orphans"
)

type Server struct {
	manager    Manager
	socketPath string
	httpServer *http.Server
}

func NewServer(m Manager, socketPath string) *Server {
	return &Server{manager: m, socketPath: socketPath}
}

func (s *Server) Start(ctx context.Context) error {
	klog.InfoS("Starting KWOK driver server", "socket", s.socketPath)
	if s.manager == nil {
		return errors.New("manager is nil")
	}
	if s.socketPath == "" {
		return errors.New("socket path is empty")
	}

	if info, err := os.Lstat(s.socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("socket path exists and is not a unix socket: %s", s.socketPath)
		}
		if err := os.Remove(s.socketPath); err != nil {
			return fmt.Errorf("remove existing socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing socket: %w", err)
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on unix socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.socketPath)
	}()

	if err := s.manager.SweepOrphans(ctx); err != nil {
		klog.ErrorS(err, "Failed to sweep orphaned resources")
		return fmt.Errorf("sweep orphaned resources: %w", err)
	}

	s.httpServer = &http.Server{Handler: s.handler()}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			klog.ErrorS(err, "HTTP server stopped unexpectedly")
		}
		cleanupErr := s.cleanup()
		if errors.Is(err, http.ErrServerClosed) {
			return cleanupErr
		}
		if cleanupErr != nil {
			return errors.Join(err, cleanupErr)
		}
		return err
	case <-ctx.Done():
		klog.InfoS("Shutting down KWOK driver server")
		shutdownCtx, cancel := shutdownContext()
		shutdownErr := s.httpServer.Shutdown(shutdownCtx)
		cleanupErr := s.manager.CleanupAll(shutdownCtx)
		cancel()
		serveResult := <-serveErr
		if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
			return serveResult
		}
		if shutdownErr != nil {
			return fmt.Errorf("shutdown HTTP server: %w", shutdownErr)
		}
		if cleanupErr != nil {
			klog.ErrorS(cleanupErr, "Failed to clean up managed resources during shutdown")
			return fmt.Errorf("cleanup managed resources: %w", cleanupErr)
		}
		klog.InfoS("Stopped KWOK driver server")
		return nil
	}
}

func (s *Server) cleanup() error {
	cleanupCtx, cancel := shutdownContext()
	defer cancel()
	return s.manager.CleanupAll(cleanupCtx)
}

func shutdownContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+provisionPath, s.provision)
	mux.HandleFunc("DELETE "+provisionPath+"/{id}", s.destroy)
	mux.HandleFunc("POST "+cleanupPath, s.cleanupAll)
	mux.HandleFunc("POST "+sweepOrphansPath, s.sweepOrphans)
	return mux
}

func (s *Server) provision(w http.ResponseWriter, r *http.Request) {
	klog.V(1).InfoS("Received provision request")
	var opts api.ProvisionOpts
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		klog.ErrorS(err, "Failed to decode provision request")
		writeError(w, http.StatusBadRequest, err)
		return
	}

	env, err := s.manager.Provision(r.Context(), &opts)
	if err != nil {
		klog.ErrorS(err, "Failed to provision environment", "testName", opts.TestName)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
	klog.InfoS("Provisioned environment", "environmentID", env.ID, "testName", opts.TestName)
}

func (s *Server) destroy(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("id")
	klog.InfoS("Received destroy request", "environmentID", envID)
	if err := s.manager.Destroy(r.Context(), envID); err != nil {
		klog.ErrorS(err, "Failed to destroy environment", "environmentID", envID)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	klog.InfoS("Destroyed environment", "environmentID", envID)
}

func (s *Server) cleanupAll(w http.ResponseWriter, r *http.Request) {
	klog.InfoS("Received cleanup request")
	if err := s.manager.CleanupAll(r.Context()); err != nil {
		klog.ErrorS(err, "Failed to clean up environments")
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	klog.InfoS("Cleaned up environments")
}

func (s *Server) sweepOrphans(w http.ResponseWriter, r *http.Request) {
	klog.InfoS("Received orphan sweep request")
	if err := s.manager.SweepOrphans(r.Context()); err != nil {
		klog.ErrorS(err, "Failed to sweep orphaned resources")
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	klog.InfoS("Swept orphaned resources")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	klog.ErrorS(err, "Returning HTTP error", "status", status)
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
