package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
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

// seedProxyHome points the config home at a fresh scratch directory holding a
// minimal registry and a wt-model home pair — the url in the home's
// config.toml, the key in a litellm.key with the given mode — the shape
// `wt-model litellm set` writes (docs/contracts/wt-model-config.sample.toml).
func seedProxyHome(t *testing.T, url, key string, keyMode os.FileMode) {
	t.Helper()
	seedConfig(t, false)
	home := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wt-model")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[litellm]\nurl = \"" + url + "\"\nkey_file = \"litellm.key\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "litellm.key")
	if err := os.WriteFile(p, []byte(key), keyMode); err != nil {
		t.Fatal(err)
	}
	// The mode is pinned explicitly: umask must not decide what a test sees.
	if err := os.Chmod(p, keyMode); err != nil {
		t.Fatal(err)
	}
}

// serveUntilListening runs `wt-model serve` on an ephemeral port over the real
// command tree with a fake deps factory and returns readers of its captured
// stderr and stdout once the announce line has landed, plus a stop function
// that cancels the run and returns the command's error. A test that parses the
// startup lines stops the run first, so no later line can interleave.
func serveUntilListening(t *testing.T) (readStderr, readStdout func() string, stop func() error) {
	t.Helper()
	cmd := rootCmd(fakeDeps)
	readErr, pwErr := newLockedBuffer()
	readOut, pwOut := newLockedBuffer()
	cmd.SetOut(pwOut)
	cmd.SetErr(pwErr)
	cmd.SetArgs([]string{"serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600)})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(readErr(), "listening on ") {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no listening line; output %q", readErr())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return readErr, readOut, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			t.Fatal("serve did not return after cancel")
			return nil
		}
	}
}

// requireNoNoteOnStdout fails the case when the fallback note reached stdout:
// ruling 7 binds it to "once per process, on stderr", so a note line on stdout
// is a two-stream firing. It runs before the stderr checks so a leak fails
// naming the leak.
func requireNoNoteOnStdout(t *testing.T, stdout string) {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "wt-model: note: ") {
			t.Fatalf("the fallback note reached stdout: %q", line)
		}
	}
}

// fakeDeps is a depsFactory whose Deps reach nothing outside the process.
func fakeDeps(cfg *config.Config, _ func() (*config.Config, error), _ *url.URL, _ string) modelserver.Deps {
	return modelserver.Deps{
		Config:    func() *config.Config { return cfg },
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
	if err == nil || !strings.Contains(err.Error(), "wt-model litellm set") {
		t.Fatalf("err = %v, want a message naming 'wt-model litellm set'", err)
	}
}

// TestServeRefusesALooseKeyFile pins the key file's mode rule at startup
// (Review Focus): a key file other users can read fails the start with the
// chmod 600 fix sentence — a secret others can read is never served, and the
// broken home never falls through to the legacy pair.
func TestServeRefusesALooseKeyFile(t *testing.T) {
	seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o644)
	_, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600))
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v, want a refusal naming chmod 600", err)
	}
}

// TestServeNotesTheLegacyFallback pins the startup note (ruling 7): a pair the
// legacy [litellm] table supplies starts the server with exactly one
// `wt-model: note: …` fallback line on stderr before the announce line (M4,
// N1) and never on stdout — once per process, on stderr — and a pair from the
// wt-model home produces none: the note fires only where the read-only
// fallback supplied the pair.
func TestServeNotesTheLegacyFallback(t *testing.T) {
	asWTModel(t)
	t.Run("legacy pair notes once", func(t *testing.T) {
		seedConfig(t, true)
		note := config.ProxyFallbackNote()
		read, readOut, stop := serveUntilListening(t)
		if err := stop(); err != nil {
			t.Fatalf("serve returned %v, want nil", err)
		}
		requireNoNoteOnStdout(t, readOut())
		lines := strings.Split(read(), "\n")
		noteAt, announceAt := -1, -1
		for i, line := range lines {
			switch {
			case strings.HasPrefix(line, "wt-model: note: "):
				if noteAt >= 0 {
					t.Errorf("a second note line: %q", line)
				} else {
					noteAt = i
				}
				if line != note {
					t.Errorf("note line = %q, want %q", line, note)
				}
			case strings.HasPrefix(line, "wt-model: listening on "):
				announceAt = i
			}
		}
		if noteAt < 0 || announceAt < 0 {
			t.Fatalf("note at %d, announce at %d; lines %q", noteAt, announceAt, lines)
		}
		if noteAt > announceAt {
			t.Errorf("note line at %d is after the announce line at %d: %q", noteAt, announceAt, lines)
		}
	})
	t.Run("home pair is silent", func(t *testing.T) {
		seedProxyHome(t, "http://127.0.0.1:4000", "sk-test", 0o600)
		read, readOut, stop := serveUntilListening(t)
		if err := stop(); err != nil {
			t.Fatalf("serve returned %v, want nil", err)
		}
		requireNoNoteOnStdout(t, readOut())
		for _, line := range strings.Split(read(), "\n") {
			if strings.HasPrefix(line, "wt-model: note: ") {
				t.Errorf("a home pair printed the fallback note: %q", line)
			}
		}
	})
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

	re := regexp.MustCompile(`wt-model: listening on (\S+) \(gateway /v1, control /control/v1\)\n`)
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

// TestAnnounceLine pins the exact startup line a later client may parse.
func TestAnnounceLine(t *testing.T) {
	want := "wt-model: listening on 127.0.0.1:8787 (gateway /v1, control /control/v1)\n"
	if got := announceLine("127.0.0.1:8787"); got != want {
		t.Errorf("announceLine = %q, want %q", got, want)
	}
}

// TestVersionFlag pins S6: --version prints "wt-model <version>" and succeeds.
func TestVersionFlag(t *testing.T) {
	out, err := run(t, "--version")
	if err != nil || out != "wt-model "+version+"\n" {
		t.Errorf("--version = %q, %v, want %q", out, err, "wt-model "+version+"\n")
	}
}

// TestServeTakesNoArguments pins Args: a stray argument is refused.
func TestServeTakesNoArguments(t *testing.T) {
	if _, err := run(t, "serve", "extra"); err == nil {
		t.Error("wt-model serve extra: no error, want a refusal")
	}
}

// TestStartupRefusalReportsOnePrefix pins that a startup refusal (a token
// file that does not exist) comes back as an error that exitcode.Report turns
// into exit 1 and exactly one "wt-model:" prefix.
func TestStartupRefusalReportsOnePrefix(t *testing.T) {
	seedConfig(t, true)
	_, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("a missing token file started the server")
	}
	var buf bytes.Buffer
	if got := exitcode.Report("wt-model", err, &buf); got != 1 {
		t.Errorf("exit = %d, want 1", got)
	}
	if want := "wt-model: " + err.Error() + "\n"; buf.String() != want || strings.Count(buf.String(), "wt-model:") != 1 {
		t.Errorf("printed %q, want %q with one prefix", buf.String(), want)
	}
}

// TestServeConfigFailureIsWordedLikeWt pins M10: a config that does not load
// reads `config error: <err> (<hint>)`, the hint config.LoadFixHint's.
func TestServeConfigFailureIsWordedLikeWt(t *testing.T) {
	seedConfig(t, true)
	cfgPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt", "config.toml")
	if err := os.WriteFile(cfgPath, []byte("this is = = not toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600))
	if err == nil {
		t.Fatal("a broken config started the server")
	}
	if !strings.HasPrefix(err.Error(), "config error: ") || !strings.HasSuffix(err.Error(), "("+config.LoadFixHint(errors.New("x"))+")") {
		t.Fatalf("err = %q, want config error: <err> (<hint>)", err)
	}
}

// TestServeRequiredFlagsAreNamedWithDashes pins that the missing-flag error
// names the flags as typed, with their placeholders.
func TestServeRequiredFlagsAreNamedWithDashes(t *testing.T) {
	_, err := run(t, "serve")
	if err == nil || !strings.Contains(err.Error(), "--listen HOST:PORT") || !strings.Contains(err.Error(), "--token-file PATH") {
		t.Fatalf("err = %v, want one naming --listen HOST:PORT and --token-file PATH", err)
	}
}

// TestServeInvalidURLRefusalNeverQuotesAnAtURL pins the read side of #640 at
// startup: a stored pair url that does not parse as a url with a host is
// refused naming the setup command, and when it holds an '@' the refusal
// shows the fixed placeholder instead of quoting it; a plain invalid url is
// still quoted, as before.
func TestServeInvalidURLRefusalNeverQuotesAnAtURL(t *testing.T) {
	seedProxyHome(t, "USERY:PASSY@hq", "sk-test", 0o600)
	_, err := run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600))
	if err == nil || !strings.Contains(err.Error(), "wt-model litellm set") || !strings.Contains(err.Error(), config.ProxyURLHidden) {
		t.Fatalf("err = %v, want the refusal with the placeholder", err)
	}
	if strings.Contains(err.Error(), "USERY") || strings.Contains(err.Error(), "PASSY") {
		t.Errorf("the refusal quotes the url: %v", err)
	}
	seedProxyHome(t, "nohost", "sk-test", 0o600)
	_, err = run(t, "serve", "--listen", "127.0.0.1:0", "--token-file", writeToken(t, 0o600))
	if err == nil || !strings.Contains(err.Error(), `"nohost"`) {
		t.Errorf("err = %v, want a plain invalid url quoted as before", err)
	}
}
