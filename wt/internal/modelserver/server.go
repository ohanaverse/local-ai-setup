package modelserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
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
}

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
}

// serverOption adjusts a Server in NewServer (test seams only).
type serverOption func(*Server)

// NewServer builds the server. It refuses an empty Token on every Listen
// address: the control API starts and stops models and the gateway spends the
// host's upstream key, so neither is ever open.
func NewServer(o Options, d Deps, opts ...serverOption) (*Server, error) {
	if o.Token == "" {
		return nil, errors.New("wt-model: a token is required: pass --token-file PATH (create one with `umask 077; head -c 32 /dev/urandom | base64 > PATH`)")
	}
	s := &Server{o: o, d: d, autoStopEvery: autoStopEvery}
	s.sessions = sessionreg.New(sessionTTL, d.Now)
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
	ctl.HandleFunc("POST /control/v1/start", s.start)
	ctl.HandleFunc("POST /control/v1/stop", s.stop)
	ctl.HandleFunc("GET /control/v1/jobs/{id}", s.job)
	ctl.HandleFunc("POST /control/v1/jobs/{id}/cancel", s.cancel)
	ctl.HandleFunc("POST /control/v1/sessions", s.register)
	ctl.HandleFunc("POST /control/v1/sessions/{id}/heartbeat", s.heartbeat)
	ctl.HandleFunc("DELETE /control/v1/sessions/{id}", s.endSession)

	root := http.NewServeMux()
	root.Handle("/control/v1/", requireToken(s.o.Token, ctl))
	// The gateway authenticates itself; it is not wrapped again.
	root.Handle("/v1/", modelgateway.New(modelgateway.Config{
		Upstream: s.o.Upstream, UpstreamKey: s.o.UpstreamKey, Token: s.o.Token,
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
// value; a body that is not valid JSON is a 400.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid request body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model"})
}

func (s *Server) catalog(w http.ResponseWriter, _ *http.Request) {
	cfg := s.d.Config()
	snap := s.d.Inventory(cfg)
	writeJSON(w, http.StatusOK, BuildCatalog(cfg, snap, s.d.Tiers(), s.sessions, s.d.Now()))
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("model")
	cfg := s.d.Config()
	snap := s.d.Inventory(cfg)
	cat := BuildCatalog(cfg, snap, s.d.Tiers(), s.sessions, s.d.Now())
	for _, row := range cat.Rows {
		if row.ID == id {
			writeJSON(w, http.StatusOK, row)
			return
		}
	}
	msg := catalog.MissingReason(cfg, &snap, id)
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
	id := s.jobs.Submit("start", req.Model, s.d.StartJob(req, s.sessions))
	writeJSON(w, http.StatusAccepted, modelapi.JobRef{ID: id})
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	var req modelapi.StopRequest
	if !decodeBody(w, r, &req) {
		return
	}
	id := s.jobs.Submit("stop", req.Model, s.d.StopJob(req, s.sessions))
	writeJSON(w, http.StatusAccepted, modelapi.JobRef{ID: id})
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	st, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_job", "no such job")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "unknown_job", "no such job")
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
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
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
		writeError(w, http.StatusGone, "unknown_session", "session is unknown or has lapsed; register again")
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
		return err
	}
	srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
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
	go func() { errc <- srv.Serve(ln) }()

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
