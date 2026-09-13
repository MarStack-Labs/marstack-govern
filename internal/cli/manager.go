package cli

import (
	"fmt"
	"log/slog"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/marstack-labs/marstack-govern/internal/api"
	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/environments"
	"github.com/marstack-labs/marstack-govern/internal/requests"
	"github.com/marstack-labs/marstack-govern/internal/simulate"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

type controllers struct {
	manager     ctrl.Manager
	preflight   *requests.Preflight
	recommender *requests.Recommender
}

func buildControllers(
	restConfig *rest.Config,
	divisions *tenancy.Store,
	requested *requests.Store,
	pricing *cost.Store,
	recommender *requests.Recommender,
	hub *api.Hub,
	logger *slog.Logger,
) (controllers, error) {
	scheme, err := tenancy.NewScheme()
	if err != nil {
		return controllers{}, err
	}

	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))

	manager, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	if err != nil {
		return controllers{}, fmt.Errorf("build controller manager: %w", err)
	}

	client := manager.GetClient()
	preflight := &requests.Preflight{Client: client}
	simulator := &simulate.Simulator{Client: client}

	registrations := []struct {
		name  string
		setup func(ctrl.Manager) error
	}{
		{"division controller", (&tenancy.Reconciler{Client: client, Scheme: scheme}).SetupWithManager},
		{"division projector", (&tenancy.Projector{Client: client, Store: divisions, Publisher: hub}).SetupWithManager},
		{"quota request controller", (&requests.RequestReconciler{
			Client:      client,
			Recommender: recommender,
			Preflight:   preflight,
			Simulator:   simulator,
		}).SetupWithManager},
		{"decision controller", (&requests.DecisionReconciler{Client: client}).SetupWithManager},
		{"request projector", (&requests.Projector{Client: client, Store: requested, Publisher: hub}).SetupWithManager},
		{"pricing policy projector", (&cost.Projector{Client: client, Store: pricing}).SetupWithManager},
		{"ephemeral environment controller", (&environments.Reconciler{Client: client, Scheme: scheme}).SetupWithManager},
	}

	for _, registration := range registrations {
		if err := registration.setup(manager); err != nil {
			return controllers{}, fmt.Errorf("register the %s: %w", registration.name, err)
		}
	}

	return controllers{manager: manager, preflight: preflight, recommender: recommender}, nil
}
