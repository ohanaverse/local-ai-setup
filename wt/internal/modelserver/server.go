package modelserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelgateway"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

const (
	// sessionTTL is how long a session lives without a heartbeat.
	sessionTTL = 60 * time.Second
	// autoStopEvery is how often the auto-stop scan runs.
	autoStopEvery = 30 * time.Second
	// maxBody caps a control request body.
	maxBody = 1 << 20
	// httpShutdownGrace bounds the HTTP server's graceful shutdown.
	httpShutdownGrace = 10 * time.Second
	// routedTTL is how long a catalog reuses LiteLLM's model list.
	routedTTL = 2 * time.Second
	// jobsShutdownGrace bounds the wait for the running job on shutdown.
	jobsShutdownGrace = 30 * time.Second
)

// Options configures a Server.
type Options struct {
	Listen      string        // host:port to listen on
	Token       string        // bearer token; required
	IdleGrace   time.Duration // how long an unused model survives before auto-stop
	Upstream    *url.URL      // the LiteLLM proxy the gateway forwards to
	UpstreamKey string        // the gateway's upstream credential
	// SubscriptionPassthrough turns on the gateway's header mode and is
	// advertised by the ping; serve sets it from the home's [gateway] table.
	SubscriptionPassthrough bool
	// ErrorLog receives the gateway proxy's transport errors; nil is the
	// standard logger. With TLS set it is also the http.Server's ErrorLog
	// (handshake errors, which name the client's address only).
	ErrorLog *log.Logger
	// TLS, when set, makes Serve speak TLS on its listener (decision 13);
	// nil is plain http, today's server exactly.
	TLS *tls.Config
	// TLSOff is the fixed reason TLS is off although configured (the
	// certificate's dates, tlsfile's DateReason), advertised by the ping as
	// tls_off; empty otherwise.
	TLSOff string
}

// tlsIdleTimeout closes idle keep-alive connections of a TLS server; a plain
// server keeps net/http's defaults (no idle timeout), as before.
const tlsIdleTimeout = 120 * time.Second

// Server is the wt-model control API and gateway.
type Server struct {
	o        Options
	d        Deps
	sessions *sessionreg.Registry
	jobs     *modeljobs.Manager
	stopper  *AutoStopper
	handler  http.Handler
	// autoStopEvery is the auto-stop scan period; tests shorten it.
	autoStopEvery time.Duration
	// routedTTL is how long the answer of Deps.RoutedIDs is reused; tests
	// shorten or lengthen it.
	routedTTL time.Duration
	routedMu  sync.Mutex
	routedAt  time.Time
	routedSet map[string]bool
}

// serverOption adjusts a Server in NewServer (test seams only).
type serverOption func(*Server)

// NewServer builds the server. It refuses a non-positive IdleGrace (a model
// would be auto-stopped the moment it went idle) and an empty Token on every Listen
// address: the control API starts and stops models and the gateway spends the
// host's upstream key, so neither is ever open.
func NewServer(o Options, d Deps, opts ...serverOption) (*Server, error) {
	if o.Token == "" {
		return nil, fmt.Errorf("a token is required; create one with '%s' and pass --token-file PATH", tokenCreateCmd)
	}
	if o.IdleGrace <= 0 {
		return nil, fmt.Errorf("the idle grace must be greater than zero, got %s", o.IdleGrace)
	}
	sessions := sessionreg.New(sessionTTL, d.Now)
	// A client session on an alias uses the alias's local target (#596):
	// the registry expands aliases from the config each read loaded.
	d = tapConfig(d, sessions)
	s := &Server{o: o, d: d, sessions: sessions, autoStopEvery: autoStopEvery, routedTTL: routedTTL}
	s.jobs = modeljobs.NewManager(d.Now)
	s.stopper = &AutoStopper{Deps: d, Sessions: s.sessions, Grace: o.IdleGrace, Jobs: s.jobs}
	for _, opt := range opts {
		opt(s)
	}
	s.handler = s.routes()
	return s, nil
}

// Handler returns the full HTTP handler (control API and gateway).
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	ctl := http.NewServeMux()
	ctl.HandleFunc("GET /control/v1/ping", s.ping)
	ctl.HandleFunc("GET /control/v1/catalog", s.catalog)
	ctl.HandleFunc("GET /control/v1/resolve", s.resolve)
	ctl.HandleFunc("GET /control/v1/candidates", s.candidates)
	ctl.HandleFunc("GET /control/v1/spend", s.spend)
	ctl.HandleFunc("POST /control/v1/start", s.start)
	ctl.HandleFunc("POST /control/v1/stop", s.stop)
	ctl.HandleFunc("GET /control/v1/jobs", s.jobsList)
	ctl.HandleFunc("GET /control/v1/jobs/{id}", s.job)
	ctl.HandleFunc("POST /control/v1/jobs/{id}/cancel", s.cancel)
	ctl.HandleFunc("POST /control/v1/sessions", s.register)
	ctl.HandleFunc("POST /control/v1/sessions/{id}/heartbeat", s.heartbeat)
	ctl.HandleFunc("DELETE /control/v1/sessions/{id}", s.endSession)

	root := http.NewServeMux()
	root.Handle("/control/v1/", requireToken(s.o.Token, controlErrors(ctl)))
	// The gateway authenticates itself; it is not wrapped again.
	root.Handle("/v1/", modelgateway.New(modelgateway.Config{
		Upstream: s.o.Upstream, UpstreamKey: s.o.UpstreamKey, Token: s.o.Token,
		AllowPaths:              modelgateway.InferencePaths,
		SubscriptionPassthrough: s.o.SubscriptionPassthrough,
		ErrorLog:                s.o.ErrorLog,
	}))
	return root
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeBody decodes a JSON request body into v; false means a 400 has been
// written. Unknown fields are ignored (a newer minor version may add optional
// request fields) and an empty body is an empty object, so v keeps its zero
// value; a body that is not valid JSON, or one carrying data after its first
// JSON value (two requests concatenated into one body would silently apply
// only the first), is a 400.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, "the request body is not valid JSON")
		return false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, "trailing data after the JSON request body")
		return false
	}
	return true
}

func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, modelapi.Ping{
		APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model",
		SubscriptionPassthrough: s.o.SubscriptionPassthrough,
		TLSOff:                  s.o.TLSOff,
	})
}

// routed is the set of ids the gateway lists, or nil when LiteLLM cannot be
// asked (fail soft: unknown, never "nothing is routed", which a restart of
// the proxy would otherwise make every client act on). The answer, success or
// failure, is reused for routedTTL so a burst of calls asks once. It holds
// routedMu across the RoutedIDs call (up to routeRequestTimeout) on purpose,
// so a burst asks LiteLLM once; the first caller's request context is the one
// used, so a client that disconnects early caches an "unknown" for routedTTL
// (it fails soft).
func (s *Server) routed(ctx context.Context) map[string]bool {
	if s.d.RoutedIDs == nil {
		return nil
	}
	s.routedMu.Lock()
	defer s.routedMu.Unlock()
	if now := s.d.Now(); !s.routedAt.IsZero() && now.Sub(s.routedAt) < s.routedTTL {
		return s.routedSet
	}
	ids, err := s.d.RoutedIDs(ctx)
	if err != nil {
		ids = nil
	} else if ids == nil {
		ids = map[string]bool{}
	}
	s.routedSet, s.routedAt = ids, s.d.Now()
	return ids
}

// invalidatingRouted wraps a start or stop job so the cached gateway list is
// dropped when the job ends: the job has just written or removed a route, and
// a catalog read within routedTTL of it would otherwise report a running model
// as unrouted (a client reads that as "needs a start") or a stopped one as
// still routed.
func (s *Server) invalidatingRouted(run modeljobs.RunFunc) modeljobs.RunFunc {
	return func(ctx context.Context, step func(string)) modelapi.JobResult {
		defer func() {
			s.routedMu.Lock()
			s.routedAt = time.Time{}
			s.routedMu.Unlock()
		}()
		return run(ctx, step)
	}
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	cfg := s.d.Config()
	snap := s.d.Inventory(cfg)
	writeJSON(w, http.StatusOK, BuildCatalog(cfg, snap, s.d.Tiers(), s.sessions, s.routed(r.Context()), s.d.Now()))
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("model")
	cfg := s.d.Config()
	snap := s.d.Inventory(cfg)
	cat := BuildCatalog(cfg, snap, s.d.Tiers(), s.sessions, s.routed(r.Context()), s.d.Now())
	for _, row := range cat.Rows {
		if row.ID == id {
			writeJSON(w, http.StatusOK, row)
			return
		}
	}
	msg := localmodels.MissingReason(cfg, &snap, id)
	if msg == "" {
		msg = fmt.Sprintf("unknown model %q", id)
	}
	writeError(w, http.StatusNotFound, modelapi.CodeUnknownModel, msg)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var req modelapi.StartRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, "model is required")
		return
	}
	// A start of a model whose start is already queued or running joins that
	// job, and so its answers (allow_replace, others): two client sessions
	// starting one model at once must not queue a second start behind the
	// first one's route restart. An alias's start is keyed by its local
	// target (startKey), so it joins a start of that model and the reverse.
	key := startKey(s.d.Config(), req.Model)
	id, _ := s.jobs.SubmitOnce("start", key, s.invalidatingRouted(s.d.StartJob(req, s.sessions)))
	writeJSON(w, http.StatusAccepted, modelapi.JobRef{ID: id})
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	var req modelapi.StopRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, "model is required")
		return
	}
	id := s.jobs.Submit("stop", req.Model, s.invalidatingRouted(s.d.StopJob(req, s.sessions)))
	writeJSON(w, http.StatusAccepted, modelapi.JobRef{ID: id})
}

// jobsList lists the queued and running jobs of ?model=ID, or of every model
// when it is absent.
func (s *Server) jobsList(w http.ResponseWriter, r *http.Request) {
	jobs := s.jobs.Active(r.URL.Query().Get("model"))
	if jobs == nil {
		jobs = []modelapi.JobStatus{}
	}
	writeJSON(w, http.StatusOK, modelapi.JobList{Jobs: jobs})
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	st, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, modelapi.CodeUnknownJob, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, modelapi.CodeUnknownJob, "no such job")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req modelapi.SessionRegister
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.sessions.Register(req.ID, req.Models); err != nil {
		writeError(w, http.StatusBadRequest, modelapi.CodeBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req modelapi.SessionBeat
	if !decodeBody(w, r, &req) {
		return
	}
	if !s.sessions.Heartbeat(r.PathValue("id"), req.Models) {
		writeError(w, http.StatusGone, modelapi.CodeUnknownSession, "the client session is unknown or has lapsed; register again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	s.sessions.End(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// Serve listens on Options.Listen, calls announce with the bound address, runs
// the auto-stop loop, and on ctx done shuts down gracefully: the HTTP server,
// then the job manager, then the pending route settles. It returns nil after a
// clean shutdown.
func (s *Server) Serve(ctx context.Context, announce func(addr string)) error {
	ln, err := net.Listen("tcp", s.o.Listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %s (pick another with --listen HOST:PORT)", s.o.Listen, listenReason(err))
	}
	srv := s.httpServer()
	if announce != nil {
		announce(ln.Addr().String())
	}
	loopCtx, stopLoop := context.WithCancel(ctx)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		s.stopper.Run(loopCtx, s.autoStopEvery)
	}()
	errc := make(chan error, 1)
	go func() {
		if srv.TLSConfig != nil {
			// The certificate is in TLSConfig; net/http adds h2 to its ALPN.
			errc <- srv.ServeTLS(ln, "", "")
			return
		}
		errc <- srv.Serve(ln)
	}()

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
	}
	stopLoop()
	<-loopDone
	hctx, hcancel := context.WithTimeout(context.Background(), httpShutdownGrace)
	defer hcancel()
	_ = srv.Shutdown(hctx)
	jctx, jcancel := context.WithTimeout(context.Background(), jobsShutdownGrace)
	defer jcancel()
	_ = s.jobs.Shutdown(jctx)
	lifecycle.WaitPendingRoutes()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

// httpServer is the http.Server Serve runs. Plain http is today's server
// exactly (Handler and ReadHeaderTimeout); only with Options.TLS does it
// gain the TLS config, IdleTimeout and the ErrorLog. Never a read or write
// timeout: prompts and streams are long.
func (s *Server) httpServer() *http.Server {
	srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
	if s.o.TLS != nil {
		srv.TLSConfig = s.o.TLS.Clone()
		srv.IdleTimeout = tlsIdleTimeout
		srv.ErrorLog = s.o.ErrorLog
	}
	return srv
}

// listenReason is why net.Listen failed, without its "listen tcp ADDR:" lead
// and "bind:" syscall label ("address already in use", "missing port in
// address"): the caller names the address itself.
func listenReason(err error) string {
	var oe *net.OpError
	if !errors.As(err, &oe) || oe.Err == nil {
		return err.Error()
	}
	var se *os.SyscallError
	if errors.As(oe.Err, &se) {
		return se.Err.Error()
	}
	var ae *net.AddrError
	if errors.As(oe.Err, &ae) {
		return ae.Err
	}
	return oe.Err.Error()
}

// statusProbe records the status and Allow header a handler would write, and
// drops its body.
type statusProbe struct {
	hdr    http.Header
	status int
}

func (p *statusProbe) Header() http.Header         { return p.hdr }
func (p *statusProbe) Write(b []byte) (int, error) { return len(b), nil }
func (p *statusProbe) WriteHeader(code int)        { p.status = code }

// controlErrors wraps the control mux so a path or method it does not route
// answers the API's error object (404 not_found, 405 method_not_allowed with
// Allow) instead of ServeMux's plain-text body. It runs behind requireToken, so
// an unauthenticated caller still gets the 401 first.
func controlErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" {
			p := &statusProbe{hdr: http.Header{}, status: http.StatusOK}
			h.ServeHTTP(p, r)
			switch p.status {
			case http.StatusNotFound:
				writeError(w, http.StatusNotFound, modelapi.CodeNotFound, modelapi.MsgNotFound)
				return
			case http.StatusMethodNotAllowed:
				if allow := p.hdr.Get("Allow"); allow != "" {
					w.Header().Set("Allow", allow)
				}
				writeError(w, http.StatusMethodNotAllowed, modelapi.CodeMethodNotAllowed, modelapi.MsgMethodNotAllowed)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
