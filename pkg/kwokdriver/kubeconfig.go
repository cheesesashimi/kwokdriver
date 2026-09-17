package kwokdriver

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/testcontainers/testcontainers-go"
)

//go:embed kubeconfig.tmpl.yaml
var kubeconfigTemplate string

type Kubeconfigs struct {
	// Kubeconfig contents which will allow a binary running on the host (e.g.,
	// kubectl or the operator binary) to connect to the KWOK server.
	HostKubeconfig []byte
	// Kubeconfig contents which will allow a binary running in a container on
	// the same container runtime network to connect to the KWOK server.
	ContainerKubeconfig []byte
}

func newKubeconfigs(ctx context.Context, con testcontainers.Container, net *testcontainers.DockerNetwork) (*Kubeconfigs, error) {
	tmpl, err := template.New("kubeconfig").Parse(kubeconfigTemplate)
	if err != nil {
		return nil, err
	}

	getKubeconfig := func(endpoint string) ([]byte, error) {
		buf := bytes.NewBuffer([]byte{})

		err = tmpl.Execute(buf, struct {
			Hostname string
		}{
			Hostname: strings.TrimPrefix(endpoint, "/"),
		})
		if err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	}

	endpoint, err := con.Endpoint(ctx, "")
	if err != nil {
		return nil, err
	}

	hostKcfg, err := getKubeconfig(endpoint)
	if err != nil {
		return nil, err
	}

	inspect, err := con.Inspect(ctx)
	if err != nil {
		return nil, err
	}

	netSettings, ok := inspect.NetworkSettings.Networks[net.Name]
	if !ok || netSettings.IPAddress.String() == "" {
		return nil, fmt.Errorf("api container has no IP on network %s", net.Name)
	}

	cntrKcfg, err := getKubeconfig(netSettings.IPAddress.String() + ":8080")
	if err != nil {
		return nil, err
	}

	kcfgs := &Kubeconfigs{
		HostKubeconfig:      hostKcfg,
		ContainerKubeconfig: cntrKcfg,
	}

	return kcfgs, nil
}
