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
	"github.com/marstack-labs/marstack-govern/internal/catalog"
	"github.com/marstack-labs/marstack-govern/internal/db"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
	"github.com/marstack-labs/marstack-govern/internal/web"
)

type serveOptions struct {
	addr        string
	databaseURL string
	kubeconfig  string
	kubeContext string
	resync      time.Duration
	logLevel    string
}

func newServeCommand() *cobra.Command {
	opts := serveOptions{
		addr:   ":8080",
		resync: 10 * time.Minute,
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

	manager, err := tenancy.NewManager(restConfig, logger)
	if err != nil {
		return err
	}

	store := catalog.NewStore(pool)
	hub := api.NewHub(0)
	watcher := kube.NewWatcher(client, opts.resync, 512)
	projector := catalog.NewProjector(store, watcher.Events(), hub, logger)

	assets, err := web.Assets()
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr: opts.addr,
		Handler: api.NewHandler(api.Options{
			Catalog: catalog.NewService(store),
			Hub:     hub,
			Web:     assets,
			Logger:  logger,
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
