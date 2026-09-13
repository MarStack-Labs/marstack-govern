package cli

import (
	"fmt"
	"log/slog"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/marstack-labs/marstack-govern/internal/admission"
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

type webhookOptions struct {
	certDir string
	port    int
}

func buildControllers(
	restConfig *rest.Config,
	divisions *tenancy.Store,
	requested *requests.Store,
	pricing *cost.Store,
	recommender *requests.Recommender,
	hub *api.Hub,
	hooks webhookOptions,
	logger *slog.Logger,
) (controllers, error) {
	scheme, err := tenancy.NewScheme()
	if err != nil {
		return controllers{}, err
	}

	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))

	options := ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	}

	if hooks.certDir != "" {
		options.WebhookServer = webhook.NewServer(webhook.Options{
			CertDir: hooks.certDir,
			Port:    hooks.port,
		})
	}

	manager, err := ctrl.NewManager(restConfig, options)
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

	if hooks.certDir == "" {
		logger.Warn("no webhook certificate configured: attribution is written by the control plane " +
			"but not enforced, so a direct kubectl write can still claim to be someone else")
	} else if err := admission.Register(manager); err != nil {
		return controllers{}, fmt.Errorf("register the attribution webhook: %w", err)
	}

	return controllers{manager: manager, preflight: preflight, recommender: recommender}, nil
}
