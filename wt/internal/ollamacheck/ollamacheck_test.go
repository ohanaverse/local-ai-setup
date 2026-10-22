package ollamacheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// fakeOllama puts an `ollama` script alone on PATH. It prints a two-model
// list and writes the arguments and the OLLAMA_HOST it was given to the
// returned file, so a test can read which daemon the command was pointed at.
func fakeOllama(t *testing.T) (sawFile string) {
	t.Helper()
	dir := t.TempDir()
	sawFile = filepath.Join(dir, "saw")
	script := "#!/bin/sh\n" +
		"echo \"$* ${OLLAMA_HOST-unset}\" >> \"" + sawFile + "\"\n" +
		"echo 'NAME              ID    SIZE      MODIFIED'\n" +
		"echo 'gemma4:9b         abc   5.0 GB    2 days ago'\n" +
		"echo 'deepseek-v4-pro   def   1.2 GB    1 week ago'\n"
	if err := os.WriteFile(filepath.Join(dir, "ollama"), []byte(script), 0o755); err != nil {
		t.Fatalf("creating fake ollama: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/bin"+string(os.PathListSeparator)+"/usr/bin")
	return sawFile
}

// ollamaConfig is a registry with one ollama provider row at baseURL.
func ollamaConfig(baseURL string) *config.Config {
	return &config.Config{Providers: []config.Provider{{ID: "ollama", Auth: config.AuthConfig{Type: "none", BaseURL: baseURL}}}}
}

// stubList replaces the seam for one test with a list holding names, and
// returns where each call's origin is recorded.
func stubList(t *testing.T, names ...string) *[]string {
	t.Helper()
	var origins []string
	t.Cleanup(StubListForTest(func(origin string) ([]string, bool, error) {
		origins = append(origins, origin)
		return names, true, nil
	}))
	return &origins
}

// TestIsOllamaModel pins which models the check applies to: those of the
// ollama provider and no other, since only they can be in `ollama list`.
func TestIsOllamaModel(t *testing.T) {
	if !IsOllamaModel(config.Model{ProviderID: "ollama"}) {
		t.Error("expected true for ollama provider")
	}
	if IsOllamaModel(config.Model{ProviderID: "openrouter"}) {
		t.Error("expected false for openrouter provider")
	}
	if IsOllamaModel(config.Model{ProviderID: "claude"}) {
		t.Error("expected false for claude provider")
	}
}

// TestAvailable runs the real exec path against a script in a temp PATH: a
// listed model is available and an unlisted one is not, and the command is
// `ollama list` pinned to the origin it was given, also when the shell's own
// OLLAMA_HOST names another daemon. Without the pin the check would ask a
// daemon the registry does not describe.
func TestAvailable(t *testing.T) {
	saw := fakeOllama(t)
	t.Setenv("OLLAMA_HOST", "http://shell-daemon.invalid:1")

	ok, err := Available("http://127.0.0.1:9", "gemma4:9b")
	if err != nil {
		t.Fatalf("Available(gemma4:9b): %v", err)
	}
	if !ok {
		t.Error("expected gemma4:9b to be available")
	}

	ok, err = Available("http://127.0.0.1:9", "missing-model")
	if err != nil {
		t.Fatalf("Available(missing-model): %v", err)
	}
	if ok {
		t.Error("expected missing-model to be unavailable")
	}

	data, err := os.ReadFile(saw)
	if err != nil {
		t.Fatalf("the fake ollama was not run: %v", err)
	}
	if got, want := string(data), "list http://127.0.0.1:9\nlist http://127.0.0.1:9\n"; got != want {
		t.Errorf("ollama was run as %q, want %q", got, want)
	}
}

// TestAvailableNotInstalled pins the answer with no ollama command on PATH:
// not available and no error, so the caller offers its "not available"
// choice instead of reporting a failed check.
func TestAvailableNotInstalled(t *testing.T) {
	// Clear PATH so ollama is not found.
	t.Setenv("PATH", "")
	ok, err := Available("http://127.0.0.1:9", "anything")
	if err != nil {
		t.Fatalf("expected no error when ollama not installed, got %v", err)
	}
	if ok {
		t.Error("expected false when ollama not installed")
	}
}

// TestAvailableReportsAFailedList pins the third answer: an ollama command
// that exits non-zero (the daemon is down) is an error, not "unavailable",
// so the caller does not tell the user to pull a model they may have.
func TestAvailableReportsAFailedList(t *testing.T) {
	t.Cleanup(StubListForTest(func(string) ([]string, bool, error) {
		return nil, true, errors.New("exit status 1")
	}))
	ok, err := Available("http://127.0.0.1:9", "gemma4:9b")
	if ok || err == nil || !strings.Contains(err.Error(), "ollama list") {
		t.Errorf("Available = %v, %v; want false and an `ollama list` error", ok, err)
	}
}

// TestAvailableStopsAListThatNeverAnswers pins that a hung `ollama list` is
// an error in bounded time, not a hang. Check runs on the TUI's update
// goroutine, so an unbounded wait freezes the launcher; the daemon may be
// remote (the point of pinning the check to the registry's address, #317),
// and a host that accepts the connection and never answers is exactly the
// case a TCP connection does not fail on its own.
func TestAvailableStopsAListThatNeverAnswers(t *testing.T) {
	dir := t.TempDir()
	// `exec` so the script does not leave a child holding the output pipe:
	// the timeout is what is under test, not the wait-delay that bounds such
	// a child.
	if err := os.WriteFile(filepath.Join(dir, "ollama"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatalf("creating a hung fake ollama: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/bin")
	old := listTimeout
	listTimeout = 100 * time.Millisecond
	t.Cleanup(func() { listTimeout = old })

	ok, err := Available("http://127.0.0.1:9", "gemma4:9b")
	if ok || err == nil || !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("Available = %v, %v; want false and a timed-out `ollama list`", ok, err)
	}
}

// TestCheckAsksTheRegistrysDaemon pins which daemon Check asks: the one the
// registry's ollama provider row names (its base_url without /v1), as
// `wt stop`, `wt model add` and `wt cloud-sync` do — and the default address
// for a row with no base_url, which is what those commands use too.
func TestCheckAsksTheRegistrysDaemon(t *testing.T) {
	for _, tc := range []struct{ name, baseURL, want string }{
		{"base_url", "http://10.1.2.3:11500/v1", "http://10.1.2.3:11500"},
		{"no base_url", "", config.OllamaBaseURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origins := stubList(t, "gemma4:9b")
			m := config.Model{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama"}
			ok, err := Check(ollamaConfig(tc.baseURL), m)
			if err != nil || !ok {
				t.Fatalf("Check = %v, %v; want true, nil", ok, err)
			}
			if len(*origins) != 1 || (*origins)[0] != tc.want {
				t.Errorf("ollama list was pinned to %v, want [%s]", *origins, tc.want)
			}
			m.ModelName = "not-pulled"
			if ok, err := Check(ollamaConfig(tc.baseURL), m); err != nil || ok {
				t.Errorf("Check(not-pulled) = %v, %v; want false, nil", ok, err)
			}
		})
	}
}

// TestCheckCannotTellWithoutADaemonAddress pins the cases where the registry
// names no daemon to ask: no ollama provider row (or no config at all), and
// a row whose base_url is not an http address. Check reports an error and
// runs no command. ollama reads an empty or unusable OLLAMA_HOST as its
// default daemon, so asking anyway would answer from a daemon the registry
// does not describe.
func TestCheckCannotTellWithoutADaemonAddress(t *testing.T) {
	m := config.Model{ID: "ollama/gemma4:9b", ModelName: "gemma4:9b", ProviderID: "ollama"}
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"no config", nil, "no ollama provider"},
		{"no provider row", &config.Config{}, "no ollama provider"},
		{"path only", ollamaConfig("/v1"), `ollama row's base_url must be http://host:port, not ""`},
		{"no scheme", ollamaConfig("localhost:11434"), `ollama row's base_url must be http://host:port, not "localhost:11434"`},
		{"blank", ollamaConfig("   "), `ollama row's base_url must be http://host:port, not "   "`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origins := stubList(t, "gemma4:9b")
			ok, err := Check(tc.cfg, m)
			if ok || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Check = %v, %v; want false and an error saying %q", ok, err, tc.want)
			}
			if len(*origins) != 0 {
				t.Errorf("ollama list was run against %v; want no command", *origins)
			}
		})
	}
}

// TestCheckSkipsOtherProviders pins that a model of another provider is
// reported available without asking ollama anything: the check is about
// `ollama list` only.
func TestCheckSkipsOtherProviders(t *testing.T) {
	origins := stubList(t)
	ok, err := Check(&config.Config{}, config.Model{ID: "openrouter/gpt-4", ModelName: "gpt-4", ProviderID: "openrouter"})
	if err != nil || !ok {
		t.Errorf("Check = %v, %v; want true, nil", ok, err)
	}
	if len(*origins) != 0 {
		t.Errorf("ollama list was run against %v; want no command", *origins)
	}
}

// TestStubListForTestRestores pins the helper other packages' tests rely on:
// the function it returns puts back whatever was there, so a test's stub
// does not outlive it and a TestMain's fail-closed default comes back.
func TestStubListForTestRestores(t *testing.T) {
	outer := StubListForTest(func(string) ([]string, bool, error) { return []string{"outer"}, true, nil })
	defer outer()
	inner := StubListForTest(func(string) ([]string, bool, error) { return []string{"inner"}, true, nil })
	if ok, _ := Available("http://127.0.0.1:9", "inner"); !ok {
		t.Error("the inner stub is not in effect")
	}
	inner()
	if ok, _ := Available("http://127.0.0.1:9", "outer"); !ok {
		t.Error("restore did not bring the outer stub back")
	}
}

// TestParseOllamaNames pins how `ollama list` output is read: the header row
// is skipped and a cloud row (SIZE "-") counts, since a cloud model is
// available to ollama.
func TestParseOllamaNames(t *testing.T) {
	out := "NAME              ID    SIZE      MODIFIED\n" +
		"gemma4:9b         abc   5.0 GB    2 days ago\n" +
		"kimi-k2.6:cloud   def   -         3 days ago\n"
	names := parseOllamaNames(out)
	if len(names) != 2 {
		t.Fatalf("parseOllamaNames: got %v, want 2 names", names)
	}
	if names[0] != "gemma4:9b" || names[1] != "kimi-k2.6:cloud" {
		t.Errorf("parseOllamaNames: got %v, want [gemma4:9b kimi-k2.6:cloud]", names)
	}
}
