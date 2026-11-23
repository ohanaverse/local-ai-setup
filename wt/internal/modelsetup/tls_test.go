package modelsetup

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

const tlsCertPath = plHome + "/tls/server.crt"

// notesHolding is the plan's notes that contain s.
func notesHolding(p Plan, s string) []string {
	var out []string
	for _, n := range p.Notes {
		if strings.Contains(n, s) {
			out = append(out, n)
		}
	}
	return out
}

// TestSetupWritesHTTPSWhenTLSConfigured pins decision 13's setup half: when
// the wt-model home's [gateway] has a valid tls_cert, the local plan's url
// (the [model-server] write and the wait step) is https:// on the loopback
// listen; with the session's ca_file unset the plan carries one note naming
// the exact line to add, and none once WT_MODEL_CA_FILE is set. An expired
// certificate gives the http:// url serve will speak plus one note naming the
// date and the renewal; a TLS setting that cannot be read refuses the plan
// (never a silent http://).
func TestSetupWritesHTTPSWhenTLSConfigured(t *testing.T) {
	on := modelmode.LocalTLS{Cert: tlsCertPath, On: true, DialName: "127.0.0.1"}
	for _, listen := range []string{"", "127.0.0.1:8787"} {
		st := baseState()
		st.LocalTLS = on
		p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}, AdminWant: AdminWant{Listen: listen}})
		if p.Want.URL != "https://127.0.0.1:8787" {
			t.Fatalf("listen %q: url = %q, want https://127.0.0.1:8787", listen, p.Want.URL)
		}
		cs, ok := stepOf(p, StepConfig)
		if !ok || !strings.Contains(cs.Detail, `url = "https://127.0.0.1:8787"`) {
			t.Errorf("config step = %+v, want it to write the https url", cs)
		}
		if strings.Contains(p.Format(), "http://127.0.0.1:8787") {
			t.Errorf("plan still names http://:\n%s", p.Format())
		}
		line := `ca_file = "` + tlsCertPath + `"`
		if n := notesHolding(p, line); len(n) != 1 || !strings.Contains(n[0], "[model-server]") || !strings.Contains(n[0], plCfgPath) {
			t.Errorf("notes = %q, want one naming %s under [model-server] in %s", p.Notes, line, plCfgPath)
		}
	}
	// A plan that (re)starts the agent waits on the https url.
	fresh := freshState()
	fresh.LocalTLS = on
	p := mustPlan(t, fresh, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if ws, ok := stepOf(p, StepWait); !ok || ws.Value != "https://127.0.0.1:8787" {
		t.Errorf("wait step = %+v, want it on https://127.0.0.1:8787", ws)
	}
	// The session already trusts a CA file: no note.
	st := baseState()
	st.LocalTLS = on
	st.CAFile = "/Users/user_name/ca.pem"
	if p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}); len(notesHolding(p, "ca_file")) != 0 {
		t.Errorf("notes = %q, want no ca_file note when WT_MODEL_CA_FILE is set", p.Notes)
	}
	// An expired certificate: plain http, and one note with the date.
	st = baseState()
	st.LocalTLS = modelmode.LocalTLS{Cert: tlsCertPath, Off: "certificate expired 2026-11-01", DialName: "127.0.0.1"}
	p = mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if p.Want.URL != "http://127.0.0.1:8787" {
		t.Errorf("expired: url = %q, want http://127.0.0.1:8787", p.Want.URL)
	}
	if n := notesHolding(p, "certificate expired 2026-11-01"); len(n) != 1 || !strings.Contains(n[0], "restart") {
		t.Errorf("expired: notes = %q, want one naming the date and the renewal", p.Notes)
	}
	if len(notesHolding(p, "ca_file")) != 0 {
		t.Errorf("expired: notes = %q, want no ca_file note while TLS is off", p.Notes)
	}
	// A setting that cannot be read refuses.
	st = baseState()
	st.LocalTLS = modelmode.LocalTLS{Err: "[gateway] tls_cert and tls_key must be set together"}
	_, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	var re *RefusalError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "must be set together") {
		t.Errorf("unreadable TLS settings: err = %v, want a refusal naming them", err)
	}
	// No [gateway] TLS: today's http url.
	if p := mustPlan(t, baseState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}); p.Want.URL != "http://127.0.0.1:8787" {
		t.Errorf("plain: url = %q", p.Want.URL)
	}
}

// TestSetupNeverWritesTLSKeys pins the Global Constraints rule for the three
// hand-edited TLS keys: a local setup applied on a scratch home with TLS on,
// through the real writers, leaves [gateway] tls_cert/tls_key exactly as
// written and adds no ca_file and no tls_* key to agent-wt's config.toml.
func TestSetupNeverWritesTLSKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	home := config.ProxyHomePath()
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	gateway := "[gateway]\ntls_cert = \"" + cert + "\"\ntls_key = \"" + key + "\"\n"
	if err := os.WriteFile(home, []byte(gateway), 0o600); err != nil {
		t.Fatal(err)
	}
	f := apEnv(t)
	env := f.Env()
	env.Config = realConfig{}
	st := freshState()
	st.LocalTLS = modelmode.ReadLocalTLS(time.Now())
	if !st.LocalTLS.On {
		t.Fatalf("LocalTLS = %+v, want on", st.LocalTLS)
	}
	st.Config.Path = config.Path()
	p := mustPlan(t, st, Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
	if !strings.HasPrefix(p.Want.URL, "https://") {
		t.Fatalf("url = %q, want https", p.Want.URL)
	}
	r := Apply(context.Background(), env, p, func(string) {})
	if r.Err != nil {
		t.Fatalf("Apply: failed %+v err %v", r.Failed, r.Err)
	}
	body, err := os.ReadFile(home)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]any
	if _, err := toml.Decode(string(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["gateway"]["tls_cert"] != cert || doc["gateway"]["tls_key"] != key || len(doc["gateway"]) != 2 {
		t.Errorf("[gateway] after setup = %v, want exactly the two keys as written", doc["gateway"])
	}
	agentWt, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatalf("agent-wt config.toml: %v", err)
	}
	for _, k := range []string{"ca_file", "tls_cert", "tls_key"} {
		if strings.Contains(string(agentWt), k) {
			t.Errorf("agent-wt config.toml gained %s:\n%s", k, agentWt)
		}
	}
	if !strings.Contains(string(agentWt), "https://127.0.0.1:8787") {
		t.Errorf("agent-wt config.toml has no https url:\n%s", agentWt)
	}
	for path, data := range f.files {
		if strings.Contains(string(data), "ca_file") || strings.Contains(string(data), "tls_cert") {
			t.Errorf("setup wrote %s holding a TLS key", path)
		}
	}
}

// netWithRealPing is the fake network with a real ping, so a wait step
// reaches an httptest TLS server.
type netWithRealPing struct{ fakeNet }

func (netWithRealPing) Ping(ctx context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error) {
	return modelping.Checked(ctx, ep)
}

// TestSetupWaitTrustsTheTLSCert pins decision 13's wait: setup's readiness
// wait on an https url trusts the home's tls_cert (with its dial name), so a
// private certificate does not end setup in a WaitError; the same wait with
// tls_cert pointing at another certificate fails with an x509 error.
func TestSetupWaitTrustsTheTLSCert(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model"})
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{tlstest.Pair(t, cert, key)}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes are expected
	srv.StartTLS()
	defer srv.Close()

	writeHome := func(certPath, keyPath string) {
		t.Helper()
		home := config.ProxyHomePath()
		if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(home, []byte("[gateway]\ntls_cert = \""+certPath+"\"\ntls_key = \""+keyPath+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wait := func() error {
		f := apEnv(t)
		f.files[plLocalTok] = []byte("tok\n")
		f.modes[plLocalTok] = 0o600
		env := f.Env()
		env.Net = netWithRealPing{fakeNet{f}}
		p := Plan{Plan: modelmode.Plan{Want: modelmode.Desired{Mode: modelclient.ModeLocal, URL: srv.URL, TokenFile: plLocalTok}}}
		return waitReady(context.Background(), env, p, Step{Kind: StepWait, Value: srv.URL})
	}
	writeHome(cert, key)
	if err := wait(); err != nil {
		t.Fatalf("wait trusting the home's tls_cert = %v", err)
	}
	other, otherKey := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	writeHome(other, otherKey)
	err := wait()
	var we *WaitError
	if !errors.As(err, &we) || !(strings.Contains(we.Message, "x509") || strings.Contains(we.Message, "certificate")) {
		t.Fatalf("wait trusting another certificate = %v, want a WaitError with an x509 message", err)
	}
}

// TestSetupReloadsServeWhenTheSchemeChanges pins review finding I1: serve
// reads [gateway] only at start, so when the local plan writes a url whose
// scheme differs from the stored local url's, or from what the running
// serve speaks (serve.json has tls_cert iff it runs TLS), and the
// LaunchAgent is loaded, the plan reloads it with the same bootout and
// bootstrap a changed plist gets (never kickstart) and waits on the new url,
// so the url written and the server converge. An unchanged scheme adds no
// step; an agent that is not loaded is bootstrapped as before.
func TestSetupReloadsServeWhenTheSchemeChanges(t *testing.T) {
	on := modelmode.LocalTLS{Cert: tlsCertPath, On: true, DialName: "127.0.0.1"}
	local := Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}

	st := baseState() // stored http://127.0.0.1:8787, agent loaded and running
	st.LocalTLS = on
	p := mustPlan(t, st, local)
	if !sameKinds(kinds(p), StepBootout, StepBootstrap, StepWait, StepConfig) {
		t.Errorf("stored http, TLS on, loaded: steps = %v, want bootout, bootstrap, wait, config", kinds(p))
	}
	if ws, _ := stepOf(p, StepWait); ws.Value != "https://127.0.0.1:8787" {
		t.Errorf("wait step = %+v, want it on the https url", ws)
	}

	st = baseState()
	st.LocalTLS = on
	st.Config.Table.URL = "https://127.0.0.1:8787"
	p = mustPlan(t, st, local)
	for _, k := range []StepKind{StepBootout, StepBootstrap, StepKickstart, StepWait} {
		if _, ok := stepOf(p, k); ok {
			t.Errorf("unchanged scheme: plan has a %s step: %v", k, kinds(p))
		}
	}

	// The running serve says otherwise (its record has no tls_cert): reload.
	st.Serve = ServeState{Seen: true, PID: st.Agent.Job.PID, TLS: false}
	p = mustPlan(t, st, local)
	if !sameKinds(kinds(p), StepBootout, StepBootstrap, StepWait) {
		t.Errorf("serve runs plain http, TLS on: steps = %v, want bootout, bootstrap, wait", kinds(p))
	}
	// A record of another process (a stale one) is not believed.
	st.Serve.PID = 999999
	p = mustPlan(t, st, local)
	if _, ok := stepOf(p, StepBootout); ok {
		t.Errorf("a stale record forced a reload: %v", kinds(p))
	}

	// Not loaded: the agent steps are the same as without TLS (bootstrap,
	// wait); only the config step's url differs.
	agentSteps := func(p Plan) []StepKind {
		var out []StepKind
		for _, k := range kinds(p) {
			if k != StepConfig {
				out = append(out, k)
			}
		}
		return out
	}
	plain := baseState()
	plain.Agent.Job = LaunchdJob{}
	plainKinds := agentSteps(mustPlan(t, plain, local))
	st = baseState()
	st.Agent.Job = LaunchdJob{}
	st.LocalTLS = on
	if got := agentSteps(mustPlan(t, st, local)); !sameKinds(got, plainKinds...) || !sameKinds(got, StepBootstrap, StepWait) {
		t.Errorf("not loaded: agent steps = %v, want %v as without TLS", got, plainKinds)
	}
}

// TestObserveReadsTheServeRecordsTLS pins where I1's running-serve fact comes
// from: Observe reads serve.json in the wt-model home (pid, and whether it
// names tls_cert), never anything else from it; no record is not seen.
func TestObserveReadsTheServeRecordsTLS(t *testing.T) {
	f := newFakeEnv(t)
	rec := plConfigHome + "/wt-model/serve.json"
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.Serve.Seen {
		t.Errorf("no record: Serve = %+v, want not seen", st.Serve)
	}
	f.files[rec] = []byte(`{"version":1,"pid":4123,"listen":"127.0.0.1:8787","token_file":"/t","started":"2026-11-17T10:00:00Z","tls_cert":"/etc/wt/server.crt"}`)
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.Serve != (ServeState{Seen: true, PID: 4123, TLS: true}) {
		t.Errorf("TLS record: Serve = %+v", st.Serve)
	}
	f.files[rec] = []byte(`{"version":1,"pid":4123,"listen":"127.0.0.1:8787","token_file":"/t","started":"2026-11-17T10:00:00Z"}`)
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.Serve != (ServeState{Seen: true, PID: 4123}) {
		t.Errorf("plain record: Serve = %+v", st.Serve)
	}
}
