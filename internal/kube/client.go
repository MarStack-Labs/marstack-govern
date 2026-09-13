package kube

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type ClientConfig struct {
	Kubeconfig string
	Context    string
	QPS        float32
	Burst      int
}

func NewClient(cfg ClientConfig) (kubernetes.Interface, *rest.Config, error) {
	restCfg, err := restConfig(cfg)
	if err != nil {
		return nil, nil, err
	}

	if cfg.QPS > 0 {
		restCfg.QPS = cfg.QPS
	}
	if cfg.Burst > 0 {
		restCfg.Burst = cfg.Burst
	}
	restCfg.UserAgent = "margov"

	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build kubernetes client: %w", err)
	}

	return client, restCfg, nil
}

func Impersonate(base *rest.Config, subject string, groups []string) (kubernetes.Interface, error) {
	if subject == "" {
		return nil, fmt.Errorf("impersonation requires a subject")
	}

	cfg := rest.CopyConfig(base)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: subject,
		Groups:   groups,
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build impersonating client for %s: %w", subject, err)
	}

	return client, nil
}

func restConfig(cfg ClientConfig) (*rest.Config, error) {
	path := cfg.Kubeconfig
	if path == "" {
		path = os.Getenv("KUBECONFIG")
	}
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := filepath.Join(home, ".kube", "config")
			if _, statErr := os.Stat(candidate); statErr == nil {
				path = candidate
			}
		}
	}

	if path == "" {
		restCfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("no kubeconfig found and not running in a cluster: %w", err)
		}
		return restCfg, nil
	}

	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: cfg.Context}

	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig %s: %w", path, err)
	}

	return restCfg, nil
}
