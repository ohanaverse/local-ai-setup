package litellm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
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

// enc encodes f or fails the test.
func enc(t *testing.T, f *File) string {
	t.Helper()
	b, err := f.encode()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
	out := enc(t, f)
	if !strings.Contains(out, "custom_param") {
		t.Fatalf("user additional_drop_params lost:\n%s", out)
	}
	if strings.Contains(out, "reasoning_effort") {
		t.Fatalf("EnsureSettings overwrote a user-managed additional_drop_params:\n%s", out)
	}
}

// TestSetRowCollapsesDuplicateRows pins that re-exposing an id whose
// model_name appears more than once leaves exactly one row (in the first
// row's position). LiteLLM load-balances across same-named rows, so a
// surviving stale duplicate would keep sending half the requests to the old
// backend after an expose.
func TestSetRowCollapsesDuplicateRows(t *testing.T) {
	// The extra row must sit inside model_list, so splice it before general_settings.
	dup := strings.Replace(baseConfig, "general_settings:", `  - model_name: ollama/old:1
    litellm_params:
      model: ollama_chat/old:1
      api_base: http://stale:11434
general_settings:`, 1)
	f, err := Open(writeConfig(t, dup))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	row, _ := BuildEntry(config.Model{ID: "ollama/old:1", ProviderID: "ollama", ModelName: "old:1"}, cfg.Providers[0])
	if err := f.SetRow("ollama/old:1", row); err != nil {
		t.Fatal(err)
	}
	ids := f.RoutedIDs()
	if len(ids) != 2 || ids[0] != "ollama/old:1" || ids[1] != "keep/me" {
		t.Fatalf("RoutedIDs = %v, want [ollama/old:1 keep/me]", ids)
	}
	if strings.Contains(enc(t, f), "stale") {
		t.Fatal("stale duplicate row survived SetRow")
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
	out := enc(t, f)
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

// TestEnsureSettingsDropsFrequencyAndPresencePenaltyOnFreshOllamaChatRows
// pins that a fresh (no additional_drop_params yet) ollama_chat/ row gets
// frequency_penalty and presence_penalty dropped alongside reasoning_effort.
// Root cause (2026-09-25, isolated via direct curl against LiteLLM): LiteLLM's
// ollama_chat bridge translates frequency_penalty/presence_penalty into
// ollama's native repeat_penalty, and for some models (observed with
// gpt-oss:20b) that translation can produce a value ollama's sampler rejects
// ("penalty_repeat must be finite and greater than 0") even when the caller
// sent 0 — copilot CLI always sends both params, so every copilot request
// against an affected model 500s. Without this, a fresh expose of any such
// model silently reintroduces the crash.
func TestEnsureSettingsDropsFrequencyAndPresencePenaltyOnFreshOllamaChatRows(t *testing.T) {
	f, _ := Open(writeConfig(t, `model_list:
  - model_name: ollama/gpt-oss:20b
    litellm_params: {model: ollama_chat/gpt-oss:20b}
`))
	f.EnsureSettings()
	out := enc(t, f)
	for _, want := range []string{"reasoning_effort", "frequency_penalty", "presence_penalty"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in additional_drop_params:\n%s", want, out)
		}
	}
}

// TestEnsureSettingsNeverExtendsAnExistingDropList pins the presence-based
// contract this task must not weaken: a row that already has
// additional_drop_params (even a single, now-incomplete entry like the
// pre-existing [reasoning_effort] this fix's own currently-deployed
// gpt-oss:20b row carries) is left exactly as-is by EnsureSettings — never
// silently widened. TestSetRowPreservesUserManagedParams already pins the
// general presence-based rule; this test pins it specifically for the case
// this task introduces (a list that predates the wider default and would
// otherwise look like an obvious "just add the missing ones" target).
func TestEnsureSettingsNeverExtendsAnExistingDropList(t *testing.T) {
	f, _ := Open(writeConfig(t, `model_list:
  - model_name: ollama/gpt-oss:20b
    litellm_params:
      model: ollama_chat/gpt-oss:20b
      additional_drop_params: [reasoning_effort]
`))
	f.EnsureSettings()
	out := enc(t, f)
	if strings.Contains(out, "frequency_penalty") || strings.Contains(out, "presence_penalty") {
		t.Fatalf("EnsureSettings widened an existing additional_drop_params list:\n%s", out)
	}
	if !strings.Contains(out, "reasoning_effort") {
		t.Fatalf("existing additional_drop_params entry lost:\n%s", out)
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
	if !strings.Contains(enc(t, f), "just-a-string") {
		t.Fatal("non-mapping row was dropped")
	}
}

// TestSavePreservesPermissions pins that the saved file keeps its mode: the
// config holds the proxy master key and provider API keys, so a save must
// keep its original mode (here 0644, distinct from the 0600 default).
func TestSavePreservesPermissions(t *testing.T) {
	p := writeConfig(t, baseConfig)
	// A mode distinct from both the fallback and CreateTemp's default (0600)
	// so the test fails if Save stops copying the original mode.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	f.RemoveRow("keep/me")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", st.Mode().Perm())
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
			if err := WithLock(context.Background(), p, func() error {
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

// TestSaveRefusesOnEncodeError pins that an encoder failure never reaches
// disk: Save must return the error and leave the existing key-bearing config
// byte-for-byte intact, and Changed must report true rather than hide it.
func TestSaveRefusesOnEncodeError(t *testing.T) {
	p := writeConfig(t, baseConfig)
	f, _ := Open(p)
	// An alias node with no target is rejected by the yaml encoder.
	mapSet(f.root(), "bad", &yaml.Node{Kind: yaml.AliasNode})
	if !f.Changed() {
		t.Fatal("Changed must be true when the document cannot be encoded")
	}
	if err := f.Save(); err == nil {
		t.Fatal("Save succeeded despite an encode failure")
	}
	got, _ := os.ReadFile(p)
	if string(got) != baseConfig {
		t.Fatalf("file modified after failed Save:\n%s", got)
	}
}

// TestOpenRefusesMultiDocument covers a two-document config.yaml: Open must
// return ErrInvalid (as Python's ruamel does), and an Apply against it must
// leave the file byte-identical. Without this, yaml.Unmarshal reads only the
// first document and Save silently deletes the rest of the user's config.
func TestOpenRefusesMultiDocument(t *testing.T) {
	body := "model_list:\n  - model_name: a/b\n    litellm_params: {model: x}\n---\n# second doc\nother: 1\n"
	p := writeConfig(t, body)
	if _, err := Open(p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open(multi-doc) err = %v, want ErrInvalid", err)
	}
	n := 0
	res, err := Apply(testConfig(), nil, []string{"a/b"}, Options{Path: p, Restart: func() []string { n++; return nil }})
	if !errors.Is(err, ErrInvalid) || res.Changed || n != 0 {
		t.Fatalf("Apply err=%v changed=%v restarts=%d", err, res.Changed, n)
	}
	if got, _ := os.ReadFile(p); string(got) != body {
		t.Fatalf("multi-doc file modified:\n%s", got)
	}
}

// TestSetRowKeepsRowComments pins that re-exposing an existing row keeps the
// comments attached to the old row node (ruamel does), so hand-written notes
// above a model are not lost every time the model starts.
func TestSetRowKeepsRowComments(t *testing.T) {
	p := writeConfig(t, "model_list:\n  # my note about gemma\n  - model_name: ollama/gemma:9b\n    litellm_params:\n      model: old\n")
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	row, _ := BuildEntry(cfg.Models[0], cfg.Providers[0])
	if err := f.SetRow("ollama/gemma:9b", row); err != nil {
		t.Fatal(err)
	}
	if out := enc(t, f); !strings.Contains(out, "# my note about gemma") {
		t.Fatalf("row comment lost:\n%s", out)
	}
}

// TestEnsureSettingsKeepsInlineComment pins that rewriting an enforced
// setting's value keeps its inline comment (drop_params: false # note), which
// ruamel preserves; otherwise every write eats the user's annotations.
func TestEnsureSettingsKeepsInlineComment(t *testing.T) {
	f, _ := Open(writeConfig(t, "model_list: []\nlitellm_settings:\n  drop_params: false # my note\n"))
	f.EnsureSettings()
	out := enc(t, f)
	if !strings.Contains(out, "drop_params: true # my note") {
		t.Fatalf("inline comment lost:\n%s", out)
	}
}

// TestIsLoopbackCaseInsensitive pins that hostnames are lower-cased before the
// loopback check (Python's urlparse().hostname does), so http://LOCALHOST:8003
// still gets use_chat_completions_api.
func TestIsLoopbackCaseInsensitive(t *testing.T) {
	f, _ := Open(writeConfig(t, "model_list:\n  - model_name: b\n    litellm_params: {model: openai/b, api_base: \"http://LOCALHOST:8003/v1\"}\n"))
	f.EnsureSettings()
	if out := enc(t, f); !strings.Contains(out, "use_chat_completions_api: true") {
		t.Fatalf("uppercase loopback host not recognised:\n%s", out)
	}
}

// TestIsLoopbackHandlesSchemelessHost pins that a schemeless api_base
// ("localhost:11434", the shape migrate.go's legacy importer can produce)
// is recognized as loopback. url.Parse alone treats "localhost:11434" as an
// opaque scheme:opaque pair (Host==""), which silently skipped the
// use_chat_completions_api fix-up for that row.
func TestIsLoopbackHandlesSchemelessHost(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"localhost:11434", true},
		{"127.0.0.1:8000", true},
		{"localhost", true},
		{"http://localhost:11434", true},
		{"example.com:1234", false},
		{"http://example.com", false},
	}
	for _, c := range cases {
		n := &yaml.Node{Kind: yaml.ScalarNode, Value: c.value}
		if got := isLoopback(n); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}

// TestWithLockHonorsContext pins that a caller with a bounded context is not
// trapped by a contended lock: flock has no timeout, so without the
// non-blocking retry the lifecycle route hook's settling bounce (15s) could
// hang past its deadline on a lock held by another process. The holder here is
// a raw flock on the same file, standing in for a concurrent wt.
func TestWithLockHonorsContext(t *testing.T) {
	p := writeConfig(t, "model_list: []\n")
	holder, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(holder.Fd()), syscall.LOCK_UN) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = WithLock(ctx, p, func() error { t.Fatal("fn must not run while the lock is held"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %v; the context must bound the wait", elapsed)
	}
}

// TestWithLockMissingConfig pins that a missing config.yaml surfaces as
// ErrMissing (the "LiteLLM not set up" message) even when its directory is
// absent too, and that no stray .lock file is left behind. Without the check
// the lock's OpenFile failed first with a raw OS error.
func TestWithLockMissingConfig(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{filepath.Join(dir, "config.yaml"), filepath.Join(dir, "nodir", "config.yaml")} {
		err := WithLock(context.Background(), p, func() error { t.Fatal("fn must not run"); return nil })
		if !errors.Is(err, ErrMissing) {
			t.Fatalf("WithLock(%s) err = %v, want ErrMissing", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml.lock")); !os.IsNotExist(err) {
		t.Fatalf("stray lock file left behind (stat err = %v)", err)
	}
}

// TestSaveFollowsSymlink pins that saving a config.yaml that is a symlink
// (dotfiles setups) rewrites the link's target and keeps the link, instead of
// replacing the symlink with a regular file that silently forks the config.
func TestSaveFollowsSymlink(t *testing.T) {
	real := writeConfig(t, baseConfig)
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f, err := Open(link)
	if err != nil {
		t.Fatal(err)
	}
	f.RemoveRow("keep/me")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced (err=%v)", err)
	}
	g, err := Open(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range g.RoutedIDs() {
		if id == "keep/me" {
			t.Fatal("target file was not updated")
		}
	}
}
