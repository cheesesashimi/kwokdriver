package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandStructure(t *testing.T) {
	command := NewCommand()

	build, _, err := command.Find([]string{"build"})
	require.NoError(t, err)
	require.NotNil(t, build)
	for _, name := range []string{"release-image", "kwok-image", "final-image"} {
		require.NotNil(t, build.Flags().Lookup(name))
	}

	start, _, err := command.Find([]string{"start"})
	require.NoError(t, err)
	require.NotNil(t, start.Flags().Lookup("kwok-cluster-image"))
	require.Equal(t, defaultSocketPath, start.Flags().Lookup("socket").DefValue)
}

func TestRequiredCommandFlags(t *testing.T) {
	for _, args := range [][]string{
		{"build"},
		{"build", "--release-image", "release"},
		{"start"},
	} {
		command := NewCommand()
		command.SetArgs(args)
		require.Error(t, command.Execute(), args)
	}
}
