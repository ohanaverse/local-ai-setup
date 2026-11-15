package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelserver"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

const minimalRegistry = `
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
auth = { type = "none", base_url = "http://localhost:11434" }

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
location = "local"
tags = ["code"]
`

// seedConfig points the config home at a fresh directory holding a minimal
// registry and, when litellm is true, a [litellm] url and key (with the agy
// agent config.Load would otherwise add, announcing a migration on stderr).
func seedConfig(t *testing.T, litellm bool) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("local-ai/registry.toml", minimalRegistry)
	if litellm {
		write("agent-wt/config.toml", "[[agents]]\nname = \"agy\"\nsupported_providers = [\"agy\"]\ndefault_provider = \"agy\"\n\n[litellm]\nenabled = true\nurl = \"http://127.0.0.1:4000\"\napi_key = \"sk-test\"\n")
	}
}

func writeToken(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("tok-abc\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeDeps is a depsFactory whose Deps reach nothing outside the process.
func fakeDeps(cfg *config.Config, _ *url.URL, _ string) modelserver.Deps {
	return modelserver.Deps{
		Config:    cfg,
		Inventory: func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} },
		Tiers:     func() tiers.Index { return tiers.Index{} },
		Now:       time.Now,
	}
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd(fakeDeps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

// TestServeRequiresListen pins that --listen is a required flag: serving
// without an address fails with cobra's required-flag error naming it.
func TestServeRequiresListen(t *testing.T) {
	_, err := run(t, "serve", "--token-file", "x")
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("err = %v, want a required-flag error naming listen", err)
	}
}

// TestServeRequiresTokenFile pins that a token is required on every listen
// address, loopback included: without --token-file cobra refuses, naming it.
func TestServeRequiresTokenFile(t *testing.T) {
	_, err := run(t, "serve", "--listen", "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "token-file") {
		t.Fatalf("err = %v, want a required-flag error naming token-file", err)
	}
}

// TestServeRefusesGroupReadableTokenFile pins that a token file other users
// can read is refused with the chmod fix, before any server starts.
func TestServeRefusesGroupReadableTokenFile(t *testing.T) {
	seedConfig(t, true)
	_, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o640))
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v, want a refusal naming chmod 600", err)
	}
}

// TestServeRejectsNonPositiveIdleGrace pins that a zero or negative
// --idle-grace is refused: it would stop every model the instant it idles.
func TestServeRejectsNonPositiveIdleGrace(t *testing.T) {
	for _, v := range []string{"0", "0s", "-5m"} {
		_, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", "x", "--idle-grace", v)
		if err == nil || !strings.Contains(err.Error(), "--idle-grace") {
			t.Errorf("--idle-grace %s: err = %v, want a refusal naming --idle-grace", v, err)
		}
	}
}

// TestServeRefusesUnconfiguredLiteLLM pins that an empty [litellm] table
// stops the server with a message naming the command that sets it.
func TestServeRefusesUnconfiguredLiteLLM(t *testing.T) {
	seedConfig(t, false)
	_, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600))
	if err == nil || !strings.Contains(err.Error(), "wt litellm set") {
		t.Fatalf("err = %v, want a message naming `wt litellm set`", err)
	}
}

// TestServeAnswersPingOnEphemeralPort pins the whole start-up path: the
// command binds an ephemeral port, announces the bound address on stderr,
// answers an authenticated ping, and returns nil when its context ends.
func TestServeAnswersPingOnEphemeralPort(t *testing.T) {
	seedConfig(t, true)
	tokenPath := writeToken(t, 0o600)
	cmd := rootCmd(fakeDeps)
	pr, pw := newLockedBuffer()
	cmd.SetOut(pw)
	cmd.SetErr(pw)
	cmd.SetArgs([]string{"serve", "--listen", "127.0.0.1:0", "--token-file", tokenPath})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	re := regexp.MustCompile(`listening on (\S+) \(gateway /v1, control /control/v1\)`)
	var addr string
	deadline := time.Now().Add(10 * time.Second)
	for addr == "" {
		if m := re.FindStringSubmatch(pr()); m != nil {
			addr = m[1]
		} else if time.Now().After(deadline) {
			t.Fatalf("no listening line; output %q", pr())
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer tok-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var ping modelapi.Ping
	err = json.NewDecoder(resp.Body).Decode(&ping)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || ping.Server != "wt-model" {
		t.Fatalf("ping = %d %+v err %v", resp.StatusCode, ping, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}

// lockedBuffer is a bytes.Buffer safe for the serving goroutine to write
// while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// newLockedBuffer returns the writer and a function reading what it holds.
func newLockedBuffer() (func() string, *lockedBuffer) {
	b := &lockedBuffer{}
	return func() string {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.buf.String()
	}, b
}
