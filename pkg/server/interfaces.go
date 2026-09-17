package server

import (
	"context"

	"github.com/cheesesashimi/kwokdriver/pkg/api"
)

type Manager interface {
	Provision(context.Context, *api.ProvisionOpts) (*api.Environment, error)
	Destroy(context.Context, string) error
	CleanupAll(context.Context) error
	SweepOrphans(context.Context) error
}
