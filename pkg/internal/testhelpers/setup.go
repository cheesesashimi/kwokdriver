package testhelpers

import (
	"fmt"
	"os"
	"os/user"
)

func PrepareForTesting() error {
	if _, exists := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !exists {
		os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
		fmt.Println("Disabled ryuk")
	}

	if _, exists := os.LookupEnv("DOCKER_HOST"); !exists {
		u, err := user.Current()
		if err != nil {
			return err
		}

		h := fmt.Sprintf("unix:///run/user/%s/podman/podman.sock", u.Uid)
		os.Setenv("DOCKER_HOST", h)
		fmt.Println("Set DOCKER_HOST=" + h)
	}

	// 	if registryAuthFile, exists := os.LookupEnv("REGISTRY_AUTH_FILE"); exists && registryAuthFile != "" {
	// 		content, err := os.ReadFile(registryAuthFile)
	// 		if err != nil {
	// 			return err
	// 		}
	//
	// 		if len(content) == 0 {
	// 			return fmt.Errorf("expected %s not to be empty", registryAuthFile)
	// 		}
	//
	// 		os.Setenv("DOCKER_AUTH_CONFIG", string(content))
	// 	}

	return nil
}
