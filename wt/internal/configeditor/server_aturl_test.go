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
// placeholder as the url to check) never pings anything, so the user:password
// never goes out as Basic auth; the server row says not checked with the
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
