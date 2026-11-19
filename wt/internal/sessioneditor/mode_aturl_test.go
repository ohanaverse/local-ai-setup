package sessioneditor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// recNet is a modelmode.Net that records every Ping and answers none:
// nothing is reachable.
type recNet struct{ pings *[]string }

func (n recNet) Ping(_ context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error) {
	*n.pings = append(*n.pings, ep.URL)
	err := &modelping.UnreachableError{Addr: ep.URL, Reason: "refused"}
	o := modelping.Classify(err)
	return modelapi.Ping{}, o.Kind, o.Message, err
}

// tmpHost is a modelmode.Host whose home is a temp directory, with no
// WT_MODEL_* variable set.
type tmpHost struct{ home string }

func (tmpHost) Getenv(string) string    { return "" }
func (h tmpHost) Home() (string, error) { return h.home, nil }

// TestModeTabNeverPingsAStoredCredentialedURL drives the tab over the real
// modelmode.Observe and BuildPlan, reading a real config.toml whose
// [model-server] is remote with "https://u:p@gpu.example:8787" and a token
// file: opening the tab and pressing c (which enters the prefilled
// placeholder as the url to check) never pings anything, so no failed-ping
// text can quote it; the server row says not checked with the fixed
// sentence; and a refuses with the fixed refusal.
func TestModeTabNeverPingsAStoredCredentialedURL(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "remote.token")
	if err := os.WriteFile(tok, []byte("tok-FILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratchConfigHome(t)
	path := config.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[model-server]\nmode = \"remote\"\nurl = \"https://u:p@gpu.example:8787\"\ntoken_file = \"" + tok + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var pings []string
	env := modelmode.DefaultEnv()
	env.Net = recNet{&pings}
	env.Host = tmpHost{home: dir}

	m := newModel(testTheme(), &config.Config{}, nil)
	m.opts = Options{StartTab: TabMode, Mode: ModeDepsFromEnv(env), LookPath: noWtModel}
	m.tab = TabMode
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
	m = runCmd(t, m, m.Init())
	if !m.mode.state.Config.URLHadCredentials {
		t.Fatalf("config.toml at %s was not read as remote with a credentialed url: %+v", path, m.mode.state.Config)
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
	if s := m.mode.state.Server; s.Checked || s.Message != "not checked: "+sentence {
		t.Errorf("Server = %+v", s)
	}

	m = keys(t, m, "a")
	const refusal = "the url contains an @ (read as a user:password); wt does not store credentials in config.toml"
	if m.mode.phase == modeConfirm || !strings.Contains(m.mode.status, refusal) {
		t.Errorf("a was not refused with the fixed refusal: phase %d status %q", m.mode.phase, m.mode.status)
	}
	if len(pings) != 0 {
		t.Errorf("pinged %q after a", pings)
	}
	for _, s := range []string{plain(m), m.mode.status} {
		if strings.Contains(s, "u:p") {
			t.Errorf("the tab shows the url's credentials:\n%s", s)
		}
	}
}
