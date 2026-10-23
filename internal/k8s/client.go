package k8s

import (
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClientset uses the in-cluster config when the process runs in a pod,
// and the default kubeconfig otherwise.
func NewClientset() (kubernetes.Interface, error) {
	cfg, err := restConfig()
	if err != nil {
		return nil, err
	}
	cfg.QPS = 20
	cfg.Burst = 40
	cfg.Timeout = 30 * time.Second
	cfg.UserAgent = "podbeacon"
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}
	return cs, nil
}

func restConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("configure cluster access: %w", err)
	}
	return cfg, nil
}
