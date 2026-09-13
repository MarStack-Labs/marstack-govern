package tenancy

import (
	"fmt"
	"log/slog"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register kubernetes types: %w", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register govern types: %w", err)
	}

	return scheme, nil
}

func NewManager(restConfig *rest.Config, logger *slog.Logger) (ctrl.Manager, error) {
	scheme, err := NewScheme()
	if err != nil {
		return nil, err
	}

	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))

	manager, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	if err != nil {
		return nil, fmt.Errorf("build controller manager: %w", err)
	}

	reconciler := &Reconciler{Client: manager.GetClient(), Scheme: manager.GetScheme()}
	if err := reconciler.SetupWithManager(manager); err != nil {
		return nil, fmt.Errorf("register the division controller: %w", err)
	}

	return manager, nil
}
