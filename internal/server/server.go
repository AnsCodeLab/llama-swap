package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mostlygeek/llama-swap/internal/chain"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/hub"
	"github.com/mostlygeek/llama-swap/internal/logmon"
	"github.com/mostlygeek/llama-swap/internal/perf"
	"github.com/mostlygeek/llama-swap/internal/router"
)

// Server owns the HTTP mux, cross-cutting middleware, and the local/peer model
// dispatch. It supersedes router.Server: it builds the local and peer routers
// directly and dispatches between them itself.
type Server struct {
	cfg config.Config

	muxlog      *logmon.Monitor
	proxylog    *logmon.Monitor
	upstreamlog *logmon.Monitor

	perf     *perf.Monitor
	inflight *inflightCounter
	metrics  *metricsMonitor
	build    BuildInfo

	local router.LocalRouter
	peer  router.Router

	hub *hub.Manager

	// configPath is the config.yaml path, used by /api/settings/* handlers
	// to persist API key and auth credential changes. Empty when the server
	// was constructed without SetConfigPath (e.g. in tests that don't need
	// settings persistence).
	configPath string

	// reloadFn requests a full config reload after /api/settings/* handlers
	// edit config.yaml, so the change is visible immediately instead of
	// waiting for the poll-based --watch-config watcher (or never, if it
	// isn't enabled). Mirrors hub.Manager's own reloadFn for the same
	// config-edit-then-refresh pattern. Nil when the server was constructed
	// without SetReloadFunc (e.g. in tests that don't need this).
	reloadFn func()

	mux     *http.ServeMux
	handler http.Handler

	shutdownCtx  context.Context
	shutdownFn   context.CancelFunc
	shuttingDown atomic.Bool
}

// modelPostJSONRoutes are endpoints with a model id in the JSON request body.
var modelPostJSONRoutes = []string{
	"/v1/chat/completions",
	"/v1/responses",
	"/v1/completions",
	"/v1/messages",
	"/v1/messages/count_tokens",
	"/v1/embeddings",
	"/reranking",
	"/rerank",
	"/v1/rerank",
	"/v1/reranking",
	"/infill",
	"/completion",
	"/v1/audio/speech",
	"/v1/audio/voices",
	"/v1/images/generations",
	"/sdapi/v1/txt2img",
	"/sdapi/v1/img2img",

	// versionless routes, the /v/ is stripped before the request is forwarded upstream
	// see issue #728
	"/v/chat/completions",
	"/v/responses",
	"/v/completions",
	"/v/messages",
	"/v/messages/count_tokens",
	"/v/embeddings",
	"/v/rerank",
	"/v/reranking",
}

// modelPostFormRoutes are multipart/form-data endpoints with a model id in the form data
var modelPostFormRoutes = []string{
	"/v1/audio/transcriptions",
	"/v1/images/edits",
}

// modelGetRoutes are model-dispatched GET endpoints (the model arrives as a
// query parameter).
var modelGetRoutes = []string{
	"/v1/audio/voices",
	"/sdapi/v1/loras",
}

// BuildInfo carries version metadata surfaced by GET /api/version.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func New(cfg config.Config, muxlog *logmon.Monitor, proxylog *logmon.Monitor, upstreamlog *logmon.Monitor, perfMon *perf.Monitor, build BuildInfo) (*Server, error) {
	var local router.LocalRouter
	var err error

	switch cfg.Routing.Router.Use {
	case "matrix":
		local, err = router.NewMatrix(cfg, proxylog, upstreamlog)
		if err != nil {
			return nil, fmt.Errorf("creating matrix router: %w", err)
		}
	default: // "group"
		local, err = router.NewGroup(cfg, proxylog, upstreamlog)
		if err != nil {
			return nil, fmt.Errorf("creating group router: %w", err)
		}
	}

	peer, err := router.NewPeer(cfg, proxylog)
	if err != nil {
		return nil, fmt.Errorf("creating peer router: %w", err)
	}

	shutdownCtx, shutdownFn := context.WithCancel(context.Background())
	s := &Server{
		cfg:         cfg,
		muxlog:      muxlog,
		proxylog:    proxylog,
		upstreamlog: upstreamlog,
		perf:        perfMon,
		inflight:    &inflightCounter{},
		metrics:     newMetricsMonitor(proxylog, cfg.MetricsMaxInMemory, cfg.CaptureBuffer),
		build:       build,
		local:       local,
		peer:        peer,
		shutdownCtx: shutdownCtx,
		shutdownFn:  shutdownFn,
	}
	s.routes()
	s.startPreload()
	return s, nil
}

// SetHubManager attaches the HuggingFace download manager. The manager is
// created once in main and outlives config reloads; call this on every
// server built, before it starts serving.
func (s *Server) SetHubManager(m *hub.Manager) {
	s.hub = m
}

// SetConfigPath records the config.yaml path so /api/settings/* handlers can
// persist changes. Call this on every server built, before it starts
// serving, mirroring SetHubManager.
func (s *Server) SetConfigPath(path string) {
	s.configPath = path
}

// SetReloadFunc registers the callback /api/settings/* handlers call after
// successfully editing config.yaml, so the change is reflected immediately
// instead of waiting for the poll-based --watch-config watcher. Call this on
// every server built, before it starts serving, mirroring hub.Manager's own
// SetReloadFunc for the same edit-then-refresh pattern.
func (s *Server) SetReloadFunc(fn func()) {
	s.reloadFn = fn
}

// localPeerHandler dispatches a model-routed request to the local or peer
// router. The model is resolved once via router.FetchContext.
func (s *Server) localPeerHandler(w http.ResponseWriter, r *http.Request) {
	stripVersionPrefix(r)

	data, err := router.FetchContext(r, s.cfg)
	if err != nil {
		router.SendError(w, r, router.ErrNoModelInContext)
		return
	}

	switch {
	case s.local.Handles(data.ModelID):
		s.proxylog.Debugf("dispatch: using local process for model: %s", data.ModelID)
		s.local.ServeHTTP(w, r)
	case s.peer.Handles(data.ModelID):
		s.proxylog.Debugf("dispatch: using peer for model: %s", data.ModelID)
		s.peer.ServeHTTP(w, r)
	default:
		router.SendError(w, r, router.ErrNoRouterFound)
	}
}

// stripVersionPrefix rewrites versionless /v/... requests to their /... form
// before forwarding upstream (issue #728).
func stripVersionPrefix(r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v/") {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v")
	}
}

// routes builds the mux, registers every route, and wraps the mux with the
// global request-log + CORS + auth middleware.
func (s *Server) routes() {
	filterMW := CreateFilterMiddleware(s.cfg)
	formFilterMW := CreateFormFilterMiddleware(s.cfg)

	// Model-dispatched routes get per-model concurrency limiting + body
	// filters + in-flight tracking + token metrics. Auth is handled once,
	// globally, by CreateGlobalAuthMiddleware in the outer handler below;
	// it now covers every route, not just this chain. concurrencyMW rejects
	// with 429 before the body filters do any rewrite work. filterMW
	// rewrites JSON bodies and formFilterMW rewrites multipart bodies; each
	// is a no-op for the other's Content-Type. Both run before the metrics
	// middleware so it buffers the rewritten body.
	modelChain := chain.New(
		CreateConcurrencyMiddleware(s.cfg),
		filterMW,
		formFilterMW,
		CreateInflightMiddleware(s.inflight),
		CreateMetricsMiddleware(s.metrics, s.cfg),
	)

	mux := http.NewServeMux()
	dispatch := http.HandlerFunc(s.localPeerHandler)

	for _, path := range modelPostJSONRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelPostFormRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelGetRoutes {
		mux.Handle("GET "+path, modelChain.Then(dispatch))
	}

	// llama-swap API + custom endpoints.
	mux.HandleFunc("GET /v1/models", s.handleListModels)
	mux.HandleFunc("GET /logs", s.handleLogs)
	mux.HandleFunc("GET /logs/stream", s.handleLogStream)
	mux.HandleFunc("GET /logs/stream/{logMonitorID...}", s.handleLogStream)

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /wol-health", handleHealth)
	mux.HandleFunc("GET /{$}", handleRootRedirect)

	// Embedded UI.
	mux.HandleFunc("GET /ui/", s.handleUI)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)

	// Prometheus metrics.
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	// Operations endpoints.
	mux.HandleFunc("GET /unload", s.handleUnload)
	mux.HandleFunc("GET /running", s.handleRunning)

	// Upstream passthrough.
	mux.HandleFunc("GET /upstream", handleUpstreamRedirect)
	mux.HandleFunc("/upstream/{upstreamPath...}", s.handleUpstream)

	// API group consumed by the UI.
	mux.HandleFunc("POST /api/models/unload", s.handleAPIUnloadAll)
	mux.HandleFunc("POST /api/models/unload/{model...}", s.handleAPIUnloadModel)
	mux.HandleFunc("GET /api/events", s.handleAPIEvents)
	mux.HandleFunc("GET /api/metrics", s.handleAPIMetrics)
	mux.HandleFunc("GET /api/performance", s.handleAPIPerformance)
	mux.HandleFunc("GET /api/version", s.handleAPIVersion)
	mux.HandleFunc("GET /api/captures/{id}", s.handleAPICapture)

	// HuggingFace hub (model downloads).
	mux.HandleFunc("GET /api/hub/popular", s.handleHubPopular)
	mux.HandleFunc("GET /api/hub/search", s.handleHubSearch)
	mux.HandleFunc("GET /api/hub/repo/{repo...}", s.handleHubRepo)
	mux.HandleFunc("GET /api/hub/detail/{repo...}", s.handleHubDetail)
	mux.HandleFunc("GET /api/hub/downloads", s.handleHubDownloads)
	mux.HandleFunc("POST /api/hub/download", s.handleHubDownload)
	mux.HandleFunc("POST /api/hub/download/cancel", s.handleHubDownloadCancel)
	mux.HandleFunc("POST /api/hub/downloads/clear", s.handleHubDownloadsClear)
	mux.HandleFunc("POST /api/hub/delete", s.handleHubDelete)
	mux.HandleFunc("GET /api/hub/hardware", s.handleHubHardware)

	// Settings: auth credentials + API key management.
	mux.HandleFunc("GET /api/settings/auth", s.handleSettingsAuthGet)
	mux.HandleFunc("POST /api/settings/auth", s.handleSettingsAuthSet)
	mux.HandleFunc("GET /api/settings/apikeys", s.handleSettingsAPIKeysList)
	mux.HandleFunc("GET /api/settings/apikeys/{id}/reveal", s.handleSettingsAPIKeyReveal)
	mux.HandleFunc("POST /api/settings/apikeys/generate", s.handleSettingsAPIKeyGenerate)
	mux.HandleFunc("POST /api/settings/apikeys/delete", s.handleSettingsAPIKeyDelete)

	s.mux = mux
	s.handler = chain.New(
		CreateRequestLogMiddleware(s.proxylog),
		CreateCORSMiddleware(), // must run before auth: answers OPTIONS preflight unauthenticated
		CreateGlobalAuthMiddleware(s.cfg),
	).Then(mux)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// CloseStreams cancels long-lived response streams (Server-Sent Events) so a
// graceful httpServer.Shutdown can drain without blocking on them. It does not
// tear down routers; call Shutdown for that. Safe to call repeatedly.
func (s *Server) CloseStreams() {
	s.shutdownFn()
}

// Shutdown stops the local and peer routers in parallel. It is idempotent;
// repeated calls return nil without re-running shutdown.
//
// Callers must drain inflight HTTP requests (httpServer.Shutdown) before
// calling this, otherwise inflight requests 502 when their processes are torn
// down. Call CloseStreams before httpServer.Shutdown so SSE streams do not
// block the drain.
func (s *Server) Shutdown(timeout time.Duration) error {
	if !s.shuttingDown.CompareAndSwap(false, true) {
		return nil
	}
	s.shutdownFn()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, rt := range []router.Router{s.local, s.peer} {
		if rt == nil {
			continue
		}
		wg.Add(1)
		go func(rt router.Router) {
			defer wg.Done()
			if err := rt.Shutdown(timeout); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(rt)
	}

	wg.Wait()
	return errors.Join(errs...)
}
