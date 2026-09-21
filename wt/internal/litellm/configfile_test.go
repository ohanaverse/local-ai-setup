package litellm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

const baseConfig = `# hand-written header comment
model_list:
  - model_name: ollama/old:1
    litellm_params:
      model: ollama_chat/old:1
      api_base: http://localhost:11434
      additional_drop_params: [custom_param]
  - model_name: keep/me
    litellm_params:
      model: openai/gpt-4o
general_settings:
  master_key: sk-secret # do not lose this
litellm_settings:
  drop_params: true
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOpenErrors pins the two refusal modes: a missing file and unparseable
// or non-mapping YAML must each return a typed error and never be
// overwritten by a later save — wt must not clobber a config it cannot read.
func TestOpenErrors(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.yaml")); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing file err = %v, want ErrMissing", err)
	}
	for _, body := range []string{"a: [unclosed", "- just\n- a list\n", ""} {
		if _, err := Open(writeConfig(t, body)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Open(%q) err = %v, want ErrInvalid", body, err)
		}
	}
}

// TestSetRowAddsAndReplacesPreservingComments covers the core edit: a new row
// is appended, an existing row is replaced in place, and hand-written comments
// and unrelated sections (general_settings, the master_key comment) survive.
// Losing those would silently break the proxy's auth or the user's notes.
func TestSetRowAddsAndReplacesPreservingComments(t *testing.T) {
	p := writeConfig(t, baseConfig)
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	row, _ := BuildEntry(cfg.Models[0], cfg.Providers[0]) // ollama/gemma:9b
	if err := f.SetRow("ollama/gemma:9b", row); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	for _, want := range []string{"# hand-written header comment", "# do not lose this", "master_key: sk-secret", "model_name: keep/me", "model_name: ollama/gemma:9b"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("saved config lost %q:\n%s", want, got)
		}
	}
	f2, _ := Open(p)
	if ids := f2.RoutedIDs(); len(ids) != 3 || ids[2] != "ollama/gemma:9b" {
		t.Fatalf("RoutedIDs = %v, want [ollama/old:1 keep/me ollama/gemma:9b]", ids)
	}
}

// TestSetRowPreservesUserManagedParams pins the presence-based rule: when a
// row is re-exposed, a user-set additional_drop_params list (an extended list
// or a deliberate empty one) and use_chat_completions_api must not be reset to
// defaults, or a deliberate opt-out would silently flip back on every start.
func TestSetRowPreservesUserManagedParams(t *testing.T) {
	p := writeConfig(t, baseConfig)
	f, _ := Open(p)
	cfg := testConfig()
	row, _ := BuildEntry(config.Model{ID: "ollama/old:1", ProviderID: "ollama", ModelName: "old:1"}, cfg.Providers[0])
	if err := f.SetRow("ollama/old:1", row); err != nil {
		t.Fatal(err)
	}
	f.EnsureSettings()
	out := string(f.encode())
	if !strings.Contains(out, "custom_param") {
		t.Fatalf("user additional_drop_params lost:\n%s", out)
	}
	if strings.Contains(out, "reasoning_effort") {
		t.Fatalf("EnsureSettings overwrote a user-managed additional_drop_params:\n%s", out)
	}
}

// TestRemoveRowAndNoOps pins removal (only the named row goes) and the no-op
// contract: removing an absent id changes nothing, so no write and no proxy
// restart follow.
func TestRemoveRowAndNoOps(t *testing.T) {
	f, _ := Open(writeConfig(t, baseConfig))
	f.RemoveRow("does/not/exist")
	if f.Changed() {
		t.Fatal("removing an absent id changed the document")
	}
	f.RemoveRow("keep/me")
	if !f.Changed() {
		t.Fatal("removing a present id did not change the document")
	}
	if ids := f.RoutedIDs(); len(ids) != 1 || ids[0] != "ollama/old:1" {
		t.Fatalf("RoutedIDs = %v", ids)
	}
}

// TestEnsureSettings pins the launcher-required settings: drop_params and the
// anthropic-messages bridge flag are value-enforced (set to true even when
// false or missing); ollama_chat/ rows gain additional_drop_params and
// loopback openai/ rows gain use_chat_completions_api only when the key is
// absent; a real (non-loopback) openai/ row is left alone. Without these the
// proxy 400s/404s for codex, claude and copilot against local backends.
func TestEnsureSettings(t *testing.T) {
	f, _ := Open(writeConfig(t, `model_list:
  - model_name: a
    litellm_params: {model: ollama_chat/a}
  - model_name: b
    litellm_params: {model: openai/b, api_base: "http://127.0.0.1:8003/v1"}
  - model_name: c
    litellm_params: {model: openai/gpt-4o, api_base: "https://api.openai.com/v1"}
litellm_settings:
  drop_params: false
`))
	f.EnsureSettings()
	out := string(f.encode())
	for _, want := range []string{"drop_params: true", "use_chat_completions_url_for_anthropic_messages: true", "reasoning_effort", "use_chat_completions_api: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "use_chat_completions_api") != 1 {
		t.Errorf("real OpenAI row must not get the bridge flag:\n%s", out)
	}
	f2, _ := Open(writeConfig(t, out))
	f2.EnsureSettings()
	if f2.Changed() {
		t.Fatal("EnsureSettings is not idempotent")
	}
}

// TestModelListDegenerateShapes pins tolerance for hand-edited configs: an
// absent or null model_list is created, a scalar model_list is refused with
// ErrInvalid (never overwritten), and non-mapping rows are preserved.
func TestModelListDegenerateShapes(t *testing.T) {
	cfg := testConfig()
	row, _ := BuildEntry(cfg.Models[0], cfg.Providers[0])
	for _, body := range []string{"general_settings: {}\n", "model_list:\n"} {
		f, _ := Open(writeConfig(t, body))
		if err := f.SetRow("ollama/gemma:9b", row); err != nil {
			t.Fatalf("SetRow on %q: %v", body, err)
		}
	}
	f, _ := Open(writeConfig(t, "model_list: nope\n"))
	if err := f.SetRow("ollama/gemma:9b", row); !errors.Is(err, ErrInvalid) {
		t.Fatalf("scalar model_list err = %v, want ErrInvalid", err)
	}
	f, _ = Open(writeConfig(t, "model_list:\n  - just-a-string\n"))
	f.RemoveRow("x")
	if !strings.Contains(string(f.encode()), "just-a-string") {
		t.Fatal("non-mapping row was dropped")
	}
}

// TestSavePreservesPermissions pins that the saved file keeps its mode: the
// config holds the proxy master key and provider API keys, so a save must
// never widen 0600 to the umask default.
func TestSavePreservesPermissions(t *testing.T) {
	p := writeConfig(t, baseConfig)
	f, _ := Open(p)
	f.RemoveRow("keep/me")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

// TestWithLockSerializes pins that concurrent read-modify-write cycles do not
// interleave: 20 goroutines each append one row under the lock and all 20
// must be present afterwards. Without the lock, two wt processes racing on
// start/stop would lose rows.
func TestWithLockSerializes(t *testing.T) {
	p := writeConfig(t, "model_list: []\n")
	cfg := testConfig()
	row, _ := BuildEntry(cfg.Models[0], cfg.Providers[0])
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a'+i)) + "/m"
			if err := WithLock(p, func() error {
				f, err := Open(p)
				if err != nil {
					return err
				}
				if err := f.SetRow(id, row); err != nil {
					return err
				}
				return f.Save()
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	f, _ := Open(p)
	if n := len(f.RoutedIDs()); n != 20 {
		t.Fatalf("rows = %d, want 20 (lost updates)", n)
	}
}

// TestDefaultPathEnv pins the path precedence: WT_LITELLM_CONFIG, then the
// legacy MODELMAN_LITELLM_CONFIG, then ~/.config/litellm/config.yaml.
func TestDefaultPathEnv(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	if got := DefaultPath(); got != "/h/.config/litellm/config.yaml" {
		t.Fatalf("default = %q", got)
	}
	t.Setenv("MODELMAN_LITELLM_CONFIG", "/legacy.yaml")
	if got := DefaultPath(); got != "/legacy.yaml" {
		t.Fatalf("legacy = %q", got)
	}
	t.Setenv("WT_LITELLM_CONFIG", "/wt.yaml")
	if got := DefaultPath(); got != "/wt.yaml" {
		t.Fatalf("wt = %q", got)
	}
}
