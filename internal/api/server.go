package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1/governv1connect"
	"github.com/marstack-labs/marstack-govern/internal/catalog"
	"github.com/marstack-labs/marstack-govern/internal/version"
)

type Options struct {
	Catalog   *catalog.Service
	Hub       *Hub
	Web       fs.FS
	Logger    *slog.Logger
	Heartbeat time.Duration
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

	if opts.Catalog != nil {
		path, handler := governv1connect.NewCatalogServiceHandler(opts.Catalog)
		mux.Handle(path, handler)
	}

	if opts.Hub != nil {
		mux.Handle("GET /v1/events", &eventStream{hub: opts.Hub, logger: logger, heartbeat: heartbeat})
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version.String() + `"}`))
	})

	if opts.Web != nil {
		mux.Handle("GET /", singlePageHandler(opts.Web))
	}

	return withLogging(mux, logger)
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
