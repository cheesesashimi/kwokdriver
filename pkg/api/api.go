package api

// Environment is the transport representation of a single isolated test run.
type Environment struct {
	Kubeconfig string `json:"kubeconfig"`
	ID         string `json:"environment_id"`
	TestName   string `json:"test_name"`
}

type ImageOrPath struct {
	Pullspec string `json:"pullspec"`
	Path     string `json:"path"`
}

type ProvisionOpts struct {
	TestName         string      `json:"test_name"`
	MCO              ImageOrPath `json:"mco"`
	MCC              ImageOrPath `json:"mcc"`
	KwokClusterImage string      `json:"kwok_cluster_image"`
	ReleaseImage     string      `json:"release_image"`
}
