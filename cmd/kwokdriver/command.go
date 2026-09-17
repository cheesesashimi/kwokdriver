package main

import (
	"fmt"
	"os"
	"os/user"

	"github.com/cheesesashimi/kwokdriver/pkg/builder"
	"github.com/cheesesashimi/kwokdriver/pkg/manager"
	"github.com/cheesesashimi/kwokdriver/pkg/server"
	"github.com/spf13/cobra"
)

const defaultSocketPath = "/tmp/kwokdriver.sock"

func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "kwokdriver",
		Short:         "Build KWOK images and run the KWOK driver server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newBuildCommand(), newStartCommand())
	return root
}

func prepare() error {
	if _, exists := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !exists {
		if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true"); err != nil {
			return err
		}
	}

	if _, exists := os.LookupEnv("DOCKER_HOST"); !exists {
		u, err := user.Current()
		if err != nil {
			return err
		}

		h := fmt.Sprintf("unix:///run/user/%s/podman/podman.sock", u.Uid)
		if err := os.Setenv("DOCKER_HOST", h); err != nil {
			return err
		}
	}

	return nil
}

func newBuildCommand() *cobra.Command {
	var opts builder.KwokImageOpts
	var finalImage string

	command := &cobra.Command{
		Use:   "build",
		Short: "Build a KWOK cluster image",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := prepare(); err != nil {
				return err
			}
			imageBuilder := builder.NewKwokImageBuilder(opts)
			if _, err := imageBuilder.Build(cmd.Context(), finalImage); err != nil {
				return fmt.Errorf("build image: %w", err)
			}
			return nil
		},
	}

	flags := command.Flags()
	flags.StringVar(&opts.ReleaseImage, "release-image", "", "OpenShift release image pullspec")
	flags.StringVar(&opts.KWOKImage, "kwok-image", "", "KWOK image pullspec")
	flags.StringVar(&finalImage, "final-image", "", "final image pullspec")
	_ = command.MarkFlagRequired("release-image")
	_ = command.MarkFlagRequired("kwok-image")
	_ = command.MarkFlagRequired("final-image")

	return command
}

func newStartCommand() *cobra.Command {
	var kwokClusterImage string
	var socketPath string

	command := &cobra.Command{
		Use:   "start",
		Short: "Start the KWOK driver server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := prepare(); err != nil {
				return err
			}
			lifecycleManager := manager.NewLifecycleManager(kwokClusterImage)
			kwokServer := server.NewServer(lifecycleManager, socketPath)
			if err := kwokServer.Start(cmd.Context()); err != nil {
				return fmt.Errorf("start server: %w", err)
			}
			return nil
		},
	}

	flags := command.Flags()
	flags.StringVar(&kwokClusterImage, "kwok-cluster-image", "", "KWOK cluster image pullspec")
	flags.StringVar(&socketPath, "socket", defaultSocketPath, "Unix socket path for the server")
	_ = command.MarkFlagRequired("kwok-cluster-image")

	return command
}
