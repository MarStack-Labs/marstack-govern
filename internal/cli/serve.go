package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/marstack-labs/marstack-govern/internal/api"
	"github.com/marstack-labs/marstack-govern/internal/audit"
	"github.com/marstack-labs/marstack-govern/internal/catalog"
	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/db"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
	"github.com/marstack-labs/marstack-govern/internal/policy"
	"github.com/marstack-labs/marstack-govern/internal/requests"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
	"github.com/marstack-labs/marstack-govern/internal/web"
)

type serveOptions struct {
	addr            string
	databaseURL     string
	kubeconfig      string
	kubeContext     string
	resync          time.Duration
	logLevel        string
	metricsURL      string
	metricsTenant   string
	recommendWindow time.Duration
	auditToken      string
	auditArchive    string
	auth            authOptions
}

func newServeCommand() *cobra.Command {
	opts := serveOptions{
		addr:            ":8080",
		resync:          10 * time.Minute,
		recommendWindow: requests.DefaultWindow,
		auth:            authOptions{secureCookies: true},
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the control plane",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd.Context(), opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.addr, "addr", opts.addr, "address to listen on")
	flags.StringVar(&opts.databaseURL, "database-url", "", "PostgreSQL connection string (falls back to GOVERN_DATABASE_URL)")
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig (falls back to KUBECONFIG, then in-cluster)")
	flags.StringVar(&opts.kubeContext, "kube-context", "", "kubeconfig context to use")
	flags.DurationVar(&opts.resync, "resync", opts.resync, "informer resync period")
	flags.StringVar(&opts.logLevel, "log-level", "info", "debug, info, warn or error")

	flags.StringVar(&opts.metricsURL, "metrics-url", "", "Prometheus or Mimir base url used to propose quota numbers")
	flags.StringVar(&opts.metricsTenant, "metrics-tenant", "", "value for X-Scope-OrgID when querying Mimir")
	flags.DurationVar(&opts.recommendWindow, "recommend-window", opts.recommendWindow, "how far back usage is read when proposing a quota")

	flags.StringVar(&opts.auditToken, "audit-token", "", "bearer token the Kubernetes audit webhook must present (falls back to GOVERN_AUDIT_TOKEN)")
	flags.StringVar(&opts.auditArchive, "audit-archive", "", "directory for append-only audit segments, ideally backed by object storage with retention")

	flags.StringVar(&opts.auth.sessionKey, "session-key", "", "32 byte session key, hex or base64 (falls back to GOVERN_SESSION_KEY)")
	flags.BoolVar(&opts.auth.secureCookies, "secure-cookies", opts.auth.secureCookies, "only send the session cookie over https")
	flags.StringVar(&opts.auth.issuer, "oidc-issuer", "", "OIDC issuer url")
	flags.StringVar(&opts.auth.clientID, "oidc-client-id", "", "OIDC client id")
	flags.StringVar(&opts.auth.clientSecret, "oidc-client-secret", "", "OIDC client secret (falls back to GOVERN_OIDC_CLIENT_SECRET)")
	flags.StringVar(&opts.auth.redirectURL, "oidc-redirect-url", "", "OIDC redirect url, ending in /auth/callback")
	flags.StringVar(&opts.auth.groupsClaim, "oidc-groups-claim", "groups", "id token claim carrying group membership")
	flags.StringVar(&opts.auth.usernameClaim, "oidc-username-claim", "email", "id token claim carrying the display name")
	flags.StringVar(&opts.auth.devIdentity, "insecure-dev-identity", "", `sign every visitor in as "subject:group-a,group-b" without an identity provider`)

	return cmd
}

func runServe(ctx context.Context, opts serveOptions) error {
	logger, err := newLogger(opts.logLevel)
	if err != nil {
		return err
	}

	dsn := opts.databaseURL
	if dsn == "" {
		dsn = os.Getenv("GOVERN_DATABASE_URL")
	}
	if dsn == "" {
		return errors.New("no database configured: pass --database-url or set GOVERN_DATABASE_URL")
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, db.Config{DSN: dsn, MaxConns: 16, MaxConnLifetime: time.Hour})
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		logger.Info("applied migrations", "versions", applied)
	}

	client, restConfig, err := kube.NewClient(kube.ClientConfig{
		Kubeconfig: opts.kubeconfig,
		Context:    opts.kubeContext,
		QPS:        50,
		Burst:      100,
	})
	if err != nil {
		return err
	}

	if err := kube.RequireDivisionCRD(client); err != nil {
		return err
	}

	sealer, registerAuth, err := buildAuth(ctx, opts.auth, logger)
	if err != nil {
		return err
	}

	store := catalog.NewStore(pool)
	divisions := tenancy.NewStore(pool)
	requested := requests.NewStore(pool)
	pricing := cost.NewStore(pool)
	authorizer := identity.NewAuthorizer(impersonatingClients(restConfig), time.Minute)
	sessions := identity.NewService(sealer, divisionAccess{store: divisions}, authorizer)
	hub := api.NewHub(0)

	metricsClient := metrics.New(metrics.Config{
		BaseURL: opts.metricsURL,
		Tenant:  opts.metricsTenant,
	})

	recommender := requests.NewRecommender(metricsClient, opts.recommendWindow)

	if !recommender.Available() {
		logger.Warn("no metrics source configured: quota requests will arrive without a proposed number")
	}

	usageReader := cost.NewReader(metricsClient)

	built, err := buildControllers(restConfig, divisions, requested, pricing, recommender, hub, logger)
	if err != nil {
		return err
	}

	manager := built.manager

	scheme, err := tenancy.NewScheme()
	if err != nil {
		return err
	}

	requestService := requests.NewService(
		manager.GetClient(),
		impersonatingRuntimeClients(restConfig, scheme),
		requested,
		recommender,
		built.preflight,
	)

	auditStore := audit.NewStore(pool)

	archive, err := audit.NewFileArchive(opts.auditArchive)
	if err != nil {
		return err
	}

	auditToken := opts.auditToken
	if auditToken == "" {
		auditToken = os.Getenv("GOVERN_AUDIT_TOKEN")
	}

	var registerAudit func(*http.ServeMux)
	if auditToken == "" {
		logger.Warn("no audit token configured: the audit webhook is closed, so the trail will stay empty")
	} else {
		var sink audit.Archive
		if archive != nil {
			sink = archive
		} else {
			logger.Warn("no audit archive configured: the chain is verifiable but only against the database")
		}

		registerAudit = audit.NewReceiver(
			audit.NewChain(auditStore, sink), auditToken, audit.DefaultFilter(), logger,
		).Route
	}

	watcher := kube.NewWatcher(client, opts.resync, 512)
	projector := catalog.NewProjector(store, watcher.Events(), hub, logger)

	assets, err := web.Assets()
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr: opts.addr,
		Handler: api.NewHandler(api.Options{
			Catalog:       catalog.NewService(store).WithScope(sessions),
			Tenancy:       tenancy.NewService(divisions).WithScope(sessions),
			Session:       sessions,
			Requests:      requestService,
			Decisions:     requests.NewDecisionService(requestService),
			FinOps:        cost.NewService(manager.GetClient(), usageReader, workloadRequests{store: store}),
			Audit:         audit.NewService(auditStore, archive),
			RegisterAudit: registerAudit,
			Policy:        policy.NewService(manager.GetClient()),
			Hub:           hub,
			Web:           assets,
			Logger:        logger,
			Sealer:        sealer,
			RegisterAuth:  registerAuth,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         api.Protocols(),
	}

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error { return watcher.Run(groupCtx) })
	group.Go(func() error { return projector.Run(groupCtx) })
	group.Go(func() error { return manager.Start(groupCtx) })

	group.Go(func() error {
		logger.Info("listening", "addr", opts.addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve http: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		<-groupCtx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(groupCtx), 15*time.Second)
		defer cancel()

		return server.Shutdown(shutdownCtx)
	})

	if err := group.Wait(); err != nil {
		return err
	}

	logger.Info("stopped")

	return nil
}

func newLogger(level string) (*slog.Logger, error) {
	var parsed slog.Level
	if err := parsed.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("unknown log level %q", level)
	}

	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parsed})), nil
}
