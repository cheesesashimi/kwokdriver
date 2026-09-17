package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/cheesesashimi/kwokdriver/pkg/api"
)

const (
	provisionPath    = "/api/v1/environments"
	cleanupPath      = "/api/v1/cleanup"
	sweepOrphansPath = "/api/v1/sweep-orphans"
)

type Client struct {
	httpClient *http.Client
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{httpClient: &http.Client{Transport: transport}}
}

var _ interface {
	Provision(context.Context, *api.ProvisionOpts) (*api.Environment, error)
	Destroy(context.Context, string) error
	CleanupAll(context.Context) error
	SweepOrphans(context.Context) error
} = (*Client)(nil)

func (c *Client) Provision(ctx context.Context, opts *api.ProvisionOpts) (*api.Environment, error) {
	body, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("encode provision request: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, provisionPath, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var env api.Environment
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode provision response: %w", err)
	}
	return &env, nil
}

func (c *Client) Destroy(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, provisionPath+"/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	return closeResponse(resp)
}

func (c *Client) CleanupAll(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPost, cleanupPath, nil)
	if err != nil {
		return err
	}
	return closeResponse(resp)
}

func (c *Client) SweepOrphans(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPost, sweepOrphansPath, nil)
	if err != nil {
		return err
	}
	return closeResponse(resp)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send %s request: %w", method, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		message, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("HTTP %s: %w", resp.Status, readErr)
		}
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, bytes.TrimSpace(message))
	}
	return resp, nil
}

func closeResponse(resp *http.Response) error {
	defer resp.Body.Close()
	_, err := io.Copy(io.Discard, resp.Body)
	return err
}
