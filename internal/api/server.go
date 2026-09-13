package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1/governv1connect"
	"github.com/marstack-labs/marstack-govern/internal/audit"
	"github.com/marstack-labs/marstack-govern/internal/catalog"
	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/requests"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
	"github.com/marstack-labs/marstack-govern/internal/version"
)

type Options struct {
	Catalog       *catalog.Service
	Tenancy       *tenancy.Service
	Session       *identity.Service
	Requests      *requests.Service
	Decisions     *requests.DecisionService
	FinOps        *cost.Service
	Audit         *audit.Service
	RegisterAudit func(*http.ServeMux)
	Hub           *Hub
	Web           fs.FS
	Logger        *slog.Logger
	Heartbeat     time.Duration
	Sealer        *identity.Sealer
	RegisterAuth  func(*http.ServeMux)
}

func NewHandler(opts Options) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	heartbeat := opts.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 20 * time.Second
	}

	mux := http.NewServeMux()

	options := []connect.HandlerOption{}
	if opts.Sealer != nil {
		options = append(options, connect.WithInterceptors(identity.RequireActor()))
	}

	if opts.RegisterAuth != nil {
		opts.RegisterAuth(mux)
	}

	if opts.Catalog != nil {
		path, handler := governv1connect.NewCatalogServiceHandler(opts.Catalog, options...)
		mux.Handle(path, handler)
	}

	if opts.Tenancy != nil {
		path, handler := governv1connect.NewTenancyServiceHandler(opts.Tenancy, options...)
		mux.Handle(path, handler)
	}

	if opts.Session != nil {
		path, handler := governv1connect.NewSessionServiceHandler(opts.Session, options...)
		mux.Handle(path, handler)
	}

	if opts.Requests != nil {
		path, handler := governv1connect.NewRequestServiceHandler(opts.Requests, options...)
		mux.Handle(path, handler)
	}

	if opts.Decisions != nil {
		path, handler := governv1connect.NewDecisionServiceHandler(opts.Decisions, options...)
		mux.Handle(path, handler)
	}

	if opts.FinOps != nil {
		path, handler := governv1connect.NewFinOpsServiceHandler(opts.FinOps, options...)
		mux.Handle(path, handler)
	}

	if opts.Audit != nil {
		path, handler := governv1connect.NewAuditServiceHandler(opts.Audit, options...)
		mux.Handle(path, handler)
	}

	if opts.RegisterAudit != nil {
		opts.RegisterAudit(mux)
	}

	if opts.Hub != nil {
		mux.Handle("GET /v1/events", &eventStream{
			hub:       opts.Hub,
			logger:    logger,
			heartbeat: heartbeat,
			sessions:  opts.Session,
			guarded:   opts.Sealer != nil,
		})
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version.String() + `"}`))
	})

	if opts.Web != nil {
		mux.Handle("GET /", singlePageHandler(opts.Web))
	}

	var handler http.Handler = mux
	if opts.Sealer != nil {
		handler = opts.Sealer.Middleware(handler)
	}

	return withLogging(handler, logger)
}

func Protocols() *http.Protocols {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	return protocols
}

func singlePageHandler(web fs.FS) http.Handler {
	files := http.FileServer(http.FS(web))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(web, trimLeadingSlash(r.URL.Path)); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func trimLeadingSlash(path string) string {
	if path == "/" || path == "" {
		return "index.html"
	}
	return path[1:]
}

func withLogging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		logger.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(started))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
