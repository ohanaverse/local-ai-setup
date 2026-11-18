package configeditor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
)

// recNet is a modelsetup.Net that records every Ping and Get and answers
// none: nothing listens and nothing is reachable.
type recNet struct{ pings, gets *[]string }

func (n recNet) Ping(_ context.Context, ep modelclient.Endpoint) (modelapi.Ping, error) {
	*n.pings = append(*n.pings, ep.URL)
	return modelapi.Ping{}, &modelclient.UnreachableError{Addr: ep.URL, Reason: "refused"}
}
func (n recNet) Get(_ context.Context, url, _ string) (int, []byte, error) {
	*n.gets = append(*n.gets, url)
	return 0, nil, errors.New("refused")
}
func (recNet) ListenerPID(context.Context, int) (int, bool, error) { return 0, false, nil }
func (recNet) ProcessStart(context.Context, int) (time.Time, error) {
	return time.Time{}, errors.New("no process")
}

// tmpHost is a modelsetup.Host whose home is a temp directory, with no
// WT_MODEL_* variable set and a non-macOS GOOS (so launchd is not asked).
type tmpHost struct{ home string }

func (tmpHost) Getenv(string) string                       { return "" }
func (h tmpHost) Home() (string, error)                    { return h.home, nil }
func (tmpHost) Executable() (string, error)                { return "", errors.New("none") }
func (tmpHost) LookPath(string) (string, error)            { return "", errors.New("none") }
func (tmpHost) Now() time.Time                             { return time.Now() }
func (tmpHost) Sleep(context.Context, time.Duration) error { return nil }
func (tmpHost) RandRead(b []byte) error                    { return nil }
func (tmpHost) GOOS() string                               { return "linux" }
func (tmpHost) UID() int                                   { return 501 }

// noLaunchd is a modelsetup.Launchd that is not available.
type noLaunchd struct{}

func (noLaunchd) Available() bool { return false }
func (noLaunchd) Print(context.Context, string) (modelsetup.LaunchdJob, error) {
	return modelsetup.LaunchdJob{}, errors.New("no launchd")
}
func (noLaunchd) Bootstrap(context.Context, string) error       { return errors.New("no launchd") }
func (noLaunchd) Bootout(context.Context, string) error         { return errors.New("no launchd") }
func (noLaunchd) Kickstart(context.Context, string, bool) error { return errors.New("no launchd") }

// noLiteLLM is a modelsetup.LiteLLMFiles with no plist and no binary.
type noLiteLLM struct{ dir string }

func (l noLiteLLM) PlistPath() string { return filepath.Join(l.dir, "litellm.plist") }
func (noLiteLLM) ConfigPath() string  { return "" }
func (noLiteLLM) ReadPlist(p string) (litellm.ProxyPlistInfo, error) {
	return litellm.ProxyPlistInfo{}, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
}
func (noLiteLLM) PlistVar(string, string) (string, bool, error) { return "", false, nil }
func (noLiteLLM) Binary() (string, bool)                        { return "", false }

// TestServerTabNeverPingsAStoredCredentialedURL drives the tab over the real
// modelsetup.Observe and BuildPlan, reading a real config.toml whose
// [model-server] is remote with "https://u:p@gpu.example:8787" and a token
// file: opening the tab and pressing c (which enters the prefilled
// placeholder as the url to check) never pings anything, so no failed-ping text
// can quote it; the server row says not checked with the
// fixed sentence; and a refuses with the fixed refusal.
func TestServerTabNeverPingsAStoredCredentialedURL(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "remote.token")
	if err := os.WriteFile(tok, []byte("tok-FILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// newTabMachine points XDG_CONFIG_HOME at a temp directory of this test
	// (restored afterwards), so config.Path() is read after it.
	models := newTabMachine(t, "").deps()
	path := config.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[model-server]\nmode = \"remote\"\nurl = \"https://u:p@gpu.example:8787\"\ntoken_file = \"" + tok + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var pings, gets []string
	env := modelsetup.DefaultEnv()
	env.Net = recNet{&pings, &gets}
	env.Host = tmpHost{home: dir}
	env.Launchd = noLaunchd{}
	env.LiteLLM = noLiteLLM{dir: dir}

	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts = Options{StartTab: TabServer, Server: ServerDepsFromEnv(env), Models: models}
	m.tab = TabServer
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
	m = runCmd(t, m, m.Init())
	if !m.server.state.Config.URLHadCredentials {
		t.Fatalf("config.toml at %s was not read as remote with a credentialed url: %+v", path, m.server.state.Config)
	}
	m = keys(t, m, "c")

	const sentence = "the url contains an @; retype it without credentials"
	if len(pings) != 0 {
		t.Errorf("pinged %q, want no ping of a url with an @", pings)
	}
	v := flat(plain(m))
	if !strings.Contains(v, flat("not checked: "+sentence)) || strings.Contains(v, "notchecked:notchecked") {
		t.Errorf("the server row does not say not checked with the fixed sentence:\n%s", plain(m))
	}
	if s := m.server.state.Server; s.Checked || s.Message != "not checked: "+sentence {
		t.Errorf("Server = %+v", s)
	}

	m = keys(t, m, "a")
	const refusal = "the url contains an @ (read as a user:password); wt does not store credentials in config.toml"
	if m.server.phase == serverConfirm || !strings.Contains(m.server.status, refusal) {
		t.Errorf("a was not refused with the fixed refusal: phase %d status %q", m.server.phase, m.server.status)
	}
	if len(pings) != 0 {
		t.Errorf("pinged %q after a", pings)
	}
	for _, s := range []string{plain(m), m.server.status} {
		if strings.Contains(s, "u:p") {
			t.Errorf("the tab shows the url's credentials:\n%s", s)
		}
	}
}

// atPairURLs are wt-model pair urls holding an '@' (issue #640): schemeless
// (which the tab's "://" scrub cannot see), with a scheme, and a password
// holding '/'. Each carries USERQ or PASSQ, which no fixed text holds.
var atPairURLs = []string{
	"USERQ:PASSQ@litellm:4000",
	"http://USERQ:PASSQ@localhost:4000",
	"USERQ:p/PASSQ@hq",
	"http://localhost:4000/?k=USERQ@PASSQ",
}

// writeAtPair writes the wt-model home's pair (under the test's
// XDG_CONFIG_HOME) by hand, as an older wt or a hand edit could have left
// it: `wt-model litellm set` now refuses such a url.
func writeAtPair(t *testing.T, raw string) {
	t.Helper()
	home := config.ProxyHomePath()
	if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[litellm]\nurl = \"" + raw + "\"\nkey_file = \"litellm.key\"\n"
	if err := os.WriteFile(home, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(home), "litellm.key"), []byte("sk-PAIRKEYQ\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestServerTabNeverShowsOrRequestsAnAtPairURL drives the tab over the real
// modelsetup.Observe with a local [model-server] and a wt-model pair whose
// url holds an '@' (#640): the pair row shows the fixed placeholder, the
// LiteLLM rows say not checked with the fixed sentence, nothing is requested
// of that url (no Net.Get at all), and neither the view nor the status holds
// USERQ or PASSQ; then a real plan from that pair (leaving local, which keeps
// the pair) reaches a confirm screen that names the pair by the placeholder
// only.
func TestServerTabNeverShowsOrRequestsAnAtPairURL(t *testing.T) {
	const notChecked = "not checked: the pair url contains an @"
	for _, raw := range atPairURLs {
		dir := t.TempDir()
		tok := filepath.Join(dir, "server.token")
		if err := os.WriteFile(tok, []byte("tok-FILE\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		models := newTabMachine(t, "").deps()
		path := config.Path()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := "[model-server]\nmode = \"local\"\nurl = \"http://127.0.0.1:8787\"\ntoken_file = \"" + tok + "\"\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		writeAtPair(t, raw)

		var pings, gets []string
		env := modelsetup.DefaultEnv()
		env.Net = recNet{&pings, &gets}
		env.Host = tmpHost{home: dir}
		env.Launchd = noLaunchd{}
		env.LiteLLM = noLiteLLM{dir: dir}

		m := newModel(testTheme(), &config.Config{}, nil)
		m.opts = Options{StartTab: TabServer, Server: ServerDepsFromEnv(env), Models: models}
		m.tab = TabServer
		m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 60})
		m = runCmd(t, m, m.Init())
		st := m.server.state
		if st.Pair.URL != modelclient.HiddenURL || !st.Pair.KeySet {
			t.Fatalf("%q: Pair = %+v, want the url hidden and the key set", raw, st.Pair)
		}
		if len(gets) != 0 {
			t.Errorf("%q: requested %q, want nothing of an '@' pair url", raw, gets)
		}
		v := flat(plain(m))
		if !strings.Contains(v, flat("LiteLLM "+modelclient.HiddenURL)) || !strings.Contains(v, flat("reachable skipped: "+notChecked)) {
			t.Errorf("%q: the pair row or the LiteLLM rows do not say it is hidden and not checked:\n%s", raw, plain(m))
		}
		for _, s := range []string{plain(m), m.server.status} {
			if strings.Contains(s, "USERQ") || strings.Contains(s, "PASSQ") {
				t.Errorf("%q: the tab shows the pair url:\n%s", raw, s)
			}
		}

		// A real plan built from that pair: leaving local keeps it.
		full := fullState()
		full.Pair, full.LiteLLM = st.Pair, st.LiteLLM
		sm := &serverMachine{states: []modelsetup.State{full}, build: true}
		m = serverEditor(t, sm, 120, 60)
		m = keys(t, m, "left", "a", "n")
		if m.server.phase != serverConfirm {
			t.Fatalf("%q: phase %d status %q, want the native plan's confirm", raw, m.server.phase, m.server.status)
		}
		cv := plain(m)
		if !strings.Contains(flat(cv), flat("keep wt-model's LiteLLM pair ("+modelclient.HiddenURL+")")) {
			t.Errorf("%q: the confirm does not name the pair by the placeholder:\n%s", raw, cv)
		}
		if strings.Contains(cv, "USERQ") || strings.Contains(cv, "PASSQ") {
			t.Errorf("%q: the confirm screen shows the pair url:\n%s", raw, cv)
		}
	}
}

// TestPairLineHidesAnAtURLItWasHanded pins the pair row's own defence: a
// PairState whose url holds an '@' (one Observe would never hand over, but
// a dep composed by hand could) is drawn as the placeholder, whole, even
// when it has no "://" for the tab's scrub to find; a plain url is drawn as
// it was.
func TestPairLineHidesAnAtURLItWasHanded(t *testing.T) {
	for _, raw := range atPairURLs {
		l := pairLine(modelsetup.PairState{URL: raw, KeySet: true})
		if l.text != "LiteLLM "+modelclient.HiddenURL+", key set" {
			t.Errorf("%q: pair row %q", raw, l.text)
		}
	}
	if l := pairLine(modelsetup.PairState{URL: "http://localhost:4000", KeySet: true}); l.text != "LiteLLM http://localhost:4000, key set" {
		t.Errorf("plain pair row %q", l.text)
	}
}
