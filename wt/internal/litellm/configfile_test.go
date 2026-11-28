package litellm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
// row is re-routed, a user-set additional_drop_params list (an extended list
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
// backend after a re-route.
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
	// Pin that the wider ollama_chat/ drop list (reasoning_effort,
	// frequency_penalty, presence_penalty) attaches only to the one
	// ollama_chat/ row ("a") — the openai/ rows ("b", loopback; "c", real
	// OpenAI) must never gain these entries (code review finding,
	// 2026-09-25: this was structurally guaranteed by the switch but had no
	// explicit pin).
	for _, want := range []string{"frequency_penalty", "presence_penalty"} {
		if n := strings.Count(out, want); n != 1 {
			t.Errorf("%q appears %d times, want exactly 1 (only on the ollama_chat/ row):\n%s", want, n, out)
		}
	}
	f2, _ := Open(writeConfig(t, out))
	f2.EnsureSettings()
	if f2.Changed() {
		t.Fatal("EnsureSettings is not idempotent")
	}
}

// TestEnsureSettingsWritesCancelOnDisconnect pins general_settings.cancel_on_disconnect:
// without it LiteLLM never notices a client that left a non-streamed request,
// so omlx keeps generating for nobody (issue #399). It is written when absent
// (or an empty null), creates the general_settings block when missing, and
// leaves every other general_settings key where it was.
func TestEnsureSettingsWritesCancelOnDisconnect(t *testing.T) {
	for name, body := range map[string]string{
		"no block":     "model_list: []\nlitellm_settings:\n  drop_params: true\n",
		"empty block":  "model_list: []\ngeneral_settings:\nlitellm_settings:\n  drop_params: true\n",
		"null key":     "model_list: []\ngeneral_settings:\n  cancel_on_disconnect:\n",
		"other keys":   "model_list: []\ngeneral_settings:\n  database_url: postgresql://u@localhost/db\n  coordination_redis:\n    host: localhost\n    port: 6379\n",
		"flow mapping": "model_list: []\ngeneral_settings: {database_url: postgresql://u@localhost/db}\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Open(writeConfig(t, body))
			if err != nil {
				t.Fatal(err)
			}
			f.EnsureSettings()
			out := enc(t, f)
			if !strings.Contains(out, "cancel_on_disconnect: true") {
				t.Fatalf("cancel_on_disconnect not enabled:\n%s", out)
			}
			if strings.Count(out, "cancel_on_disconnect") != 1 || strings.Count(out, "general_settings:") != 1 {
				t.Errorf("duplicate key or block:\n%s", out)
			}
			for _, keep := range []string{"database_url", "coordination_redis", "port: 6379"} {
				if strings.Contains(body, keep) && !strings.Contains(out, keep) {
					t.Errorf("lost %q:\n%s", keep, out)
				}
			}
			f2, _ := Open(writeConfig(t, out))
			f2.EnsureSettings()
			if f2.Changed() {
				t.Errorf("EnsureSettings is not idempotent for %q:\n%s", name, enc(t, f2))
			}
		})
	}
}

// TestEnsureSettingsKeepsAnExplicitCancelOnDisconnect pins the one opt-out:
// a hand-written value (false) survives, like the other presence-based keys.
func TestEnsureSettingsKeepsAnExplicitCancelOnDisconnect(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: []\ngeneral_settings:\n  cancel_on_disconnect: false\n  store_model_in_db: true\nlitellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"+affinitySettings))
	if err != nil {
		t.Fatal(err)
	}
	f.EnsureSettings()
	if f.Changed() {
		t.Fatalf("an explicit cancel_on_disconnect: false was rewritten:\n%s", enc(t, f))
	}
	if out := enc(t, f); !strings.Contains(out, "cancel_on_disconnect: false") {
		t.Fatalf("explicit false lost:\n%s", out)
	}
}

// TestEnsureSettingsToleratesAMalformedGeneralSettings pins that a hand-edited
// general_settings that is not a mapping is left alone instead of panicking or
// being replaced (the same tolerance litellm_settings gets).
func TestEnsureSettingsToleratesAMalformedGeneralSettings(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: []\ngeneral_settings: [a, b]\n"))
	if err != nil {
		t.Fatal(err)
	}
	f.EnsureSettings()
	if out := enc(t, f); strings.Contains(out, "cancel_on_disconnect") {
		t.Fatalf("malformed general_settings was rewritten:\n%s", out)
	}
}

// TestEnsureSettingsKeepsTheAnchorOnAnEmptyBlock pins that an empty block
// carrying a YAML anchor is filled in place: replacing the node would drop the
// anchor and leave every alias that refers to it unresolvable.
func TestEnsureSettingsKeepsTheAnchorOnAnEmptyBlock(t *testing.T) {
	for _, key := range []string{"general_settings", "litellm_settings"} {
		f, err := Open(writeConfig(t, key+": &blk\nother: *blk\nmodel_list: []\n"))
		if err != nil {
			t.Fatal(err)
		}
		f.EnsureSettings()
		out := enc(t, f)
		if !strings.Contains(out, "&blk") || !strings.Contains(out, "*blk") {
			t.Errorf("%s: anchor lost, the alias no longer resolves:\n%s", key, out)
		}
		if _, err := Open(writeConfig(t, out)); err != nil {
			t.Errorf("%s: output does not reopen: %v\n%s", key, err, out)
		}
	}
}

// TestEnsureSettingsHonorsMergedGeneralSettings pins that general_settings
// reached through YAML merge keys counts as present, like the database_url
// lookup in dburl.go: an inherited cancel_on_disconnect: false is the opt-out
// and is not overridden by a literal true, and a general_settings that a
// root-level merge supplies is not shadowed by a fresh literal block (which
// would replace it wholesale and drop its database_url).
func TestEnsureSettingsHonorsMergedGeneralSettings(t *testing.T) {
	const settled = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n" + affinitySettings
	for name, body := range map[string]string{
		"inherited false": "base: &b {cancel_on_disconnect: false}\ngeneral_settings:\n  <<: *b\n  database_url: postgresql://u@localhost/db\n  store_model_in_db: true\n" + settled,
		"inherited true":  "base: &b {cancel_on_disconnect: true}\ngeneral_settings:\n  <<: *b\n  database_url: postgresql://u@localhost/db\n  store_model_in_db: true\n" + settled,
		"root merge":      "base: &b\n  general_settings:\n    database_url: postgresql://u@localhost/db\n<<: *b\n" + settled,
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Open(writeConfig(t, body))
			if err != nil {
				t.Fatal(err)
			}
			f.EnsureSettings()
			if f.Changed() {
				t.Fatalf("EnsureSettings rewrote a config whose general_settings comes through a merge key:\n%s", enc(t, f))
			}
		})
	}
}

// TestSettingsWarningsNameTheLayoutsThatSkipCancelOnDisconnect pins that the
// two layouts EnsureSettings leaves alone (general_settings as a whole-value
// alias, or supplied by a root merge key) are reported, so the user learns the
// cancel protection is off, and that an ordinary layout reports nothing.
func TestSettingsWarningsNameTheLayoutsThatSkipCancelOnDisconnect(t *testing.T) {
	const settled = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n" + affinitySettings
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"alias":      {"base: &b {database_url: postgresql://u@localhost/db}\ngeneral_settings: *b\n" + settled, "is a YAML alias"},
		"root merge": {"base: &b\n  general_settings:\n    database_url: postgresql://u@localhost/db\n<<: *b\n" + settled, "root merge key"},
		"plain":      {"general_settings:\n  database_url: postgresql://u@localhost/db\n" + settled, ""},
		"absent":     {settled, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Open(writeConfig(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			got := f.settingsWarnings()
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("settingsWarnings = %q, want none", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.want) || !strings.Contains(got[0], "cancel_on_disconnect") {
				t.Fatalf("settingsWarnings = %q, want one naming %q and cancel_on_disconnect", got, tc.want)
			}
			f.EnsureSettings()
			if f.Changed() {
				t.Fatalf("EnsureSettings wrote through a shared general_settings:\n%s", enc(t, f))
			}
		})
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
// against an affected model 500s. Without this, a fresh route of any such
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
	// Pin "left exactly as-is", not just "didn't gain these two strings"
	// (code review finding, 2026-09-25): re-running EnsureSettings on the
	// row this call already touched (for litellm_settings, which this
	// config lacked) must report no further change — a stray rewrite of
	// the row's formatting or ordering would fail this even though the
	// substring checks above would still pass.
	f2, _ := Open(writeConfig(t, out))
	f2.EnsureSettings()
	if f2.Changed() {
		t.Fatalf("EnsureSettings is not idempotent on a row with a pre-existing additional_drop_params list:\n%s", enc(t, f2))
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
	res, err := ApplyChange(testConfig(), Change{Remove: []string{"a/b"}}, Options{Path: p, Restart: func() []string { n++; return nil }})
	if !errors.Is(err, ErrInvalid) || res.Changed || n != 0 {
		t.Fatalf("ApplyChange err=%v changed=%v restarts=%d", err, res.Changed, n)
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

// TestIsLoopbackReadsAHostAsProviderOriginDoes pins that the route settings
// and config.Provider.Origin share one reading of "this machine" (#348).
// Origin takes an mtplx base_url on 0.0.0.0 or "localhost." for the server wt
// starts and routes it to port 8003. With a narrower list of its own here,
// that route was written without use_chat_completions_api, and codex's
// /v1/responses call went straight to a server that has only
// /v1/chat/completions: a 404, then 429s from the router's cooldown.
func TestIsLoopbackReadsAHostAsProviderOriginDoes(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1/v1", "http://localhost", "http://LOCALHOST", "http://localhost./v1",
		"http://0.0.0.0/v1", "http://[::]/v1",
	} {
		p := config.Provider{ID: "mtplx", Auth: config.AuthConfig{Type: "none", BaseURL: base}}
		apiBase := p.Origin() + "/v1"
		if !strings.Contains(apiBase, ":8003/") {
			t.Fatalf("%q: api_base = %q, want the port wt serves mtplx on (the case this test is about)", base, apiBase)
		}
		if !isLoopback(&yaml.Node{Kind: yaml.ScalarNode, Value: apiBase}) {
			t.Errorf("isLoopback(%q) = false for a base_url Origin takes for this machine (%q)", apiBase, base)
		}
	}
	// This machine, though not an address wt serves mtplx on: a server the
	// user runs there is still a local one.
	for _, apiBase := range []string{"http://[::1]:8003/v1", "http://127.0.0.2:8003/v1"} {
		if !isLoopback(&yaml.Node{Kind: yaml.ScalarNode, Value: apiBase}) {
			t.Errorf("isLoopback(%q) = false, want true: this machine", apiBase)
		}
	}
	for _, apiBase := range []string{"http://10.0.0.5:8003/v1", "https://mtplx.example/v1", "http://localhost.example:8003/v1"} {
		if isLoopback(&yaml.Node{Kind: yaml.ScalarNode, Value: apiBase}) {
			t.Errorf("isLoopback(%q) = true, want false: not this machine", apiBase)
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
	if slices.Contains(g.RoutedIDs(), "keep/me") {
		t.Fatal("target file was not updated")
	}
}

// TestRowsReportsManaged pins Rows(): file order, and Managed only for rows
// whose model_info carries wt_managed: true — a hand-written row (no marker,
// or a non-bool value) is never reported as wt's.
func TestRowsReportsManaged(t *testing.T) {
	f, err := Open(writeConfig(t, `model_list:
  - model_name: hand/alias
    litellm_params: {model: openrouter/x}
  - model_name: ollama/a:1
    litellm_params: {model: ollama_chat/a:1}
    model_info: {wt_managed: true}
  - model_name: odd/one
    litellm_params: {model: openai/x}
    model_info: {wt_managed: "yes"}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"hand/alias", false}, {"ollama/a:1", true}, {"odd/one", false}}
	if got := f.Rows(); !slices.Equal(got, want) {
		t.Fatalf("Rows() = %v, want %v", got, want)
	}
}

// TestRowsToleratesNonSequenceModelList pins that a hand-edited
// `model_list: nope` (a scalar, not a list) leaves the row readers empty
// instead of panicking or fabricating a row: `wt-model litellm list` and `sync` must
// not crash on a config a user can trivially mistype, and no phantom row may be
// classified as wt's.
func TestRowsToleratesNonSequenceModelList(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: nope\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Rows(); len(got) != 0 {
		t.Fatalf("Rows() = %v, want none for a non-sequence model_list", got)
	}
	if got := f.row("x"); got != nil {
		t.Fatalf("row(%q) = %v, want nil", "x", got)
	}
	if got := f.RoutedIDs(); len(got) != 0 {
		t.Fatalf("RoutedIDs() = %v, want none", got)
	}
	// Nothing named x exists, so there is no row to classify — and the guard
	// must not have invented one.
	if IsManaged(f.row("x")) {
		t.Fatal("IsManaged(nil) = true, want false")
	}
}

// TestIsManagedToleratesScalarModelInfo pins that a hand-edited row whose
// model_info is a scalar (`model_info: legacy`) reads as hand-written rather
// than panicking: one mistyped key must not crash every `wt litellm` command on
// that file, and the row must not be misclassified as wt's.
func TestIsManagedToleratesScalarModelInfo(t *testing.T) {
	f, err := Open(writeConfig(t, `model_list:
  - model_name: a/b
    litellm_params: {model: x}
    model_info: legacy
`))
	if err != nil {
		t.Fatal(err)
	}
	row := f.row("a/b")
	if row == nil {
		t.Fatal("row a/b not found")
	}
	if IsManaged(row) {
		t.Fatal("scalar model_info was read as wt-managed")
	}
	if got := f.Rows(); !slices.Equal(got, []RowInfo{{"a/b", false}}) {
		t.Fatalf("Rows() = %v, want the row reported as hand-written", got)
	}
}

// TestIsManagedRequiresStrictBool pins that only a literal `wt_managed: true`
// marks a row as wt's: the string "yes" and the integer 1 that a hand-edited
// file might use must read as hand-written, or sync could remove a route the
// user wrote themselves.
func TestIsManagedRequiresStrictBool(t *testing.T) {
	f, err := Open(writeConfig(t, `model_list:
  - model_name: str/yes
    litellm_params: {model: x}
    model_info: {wt_managed: "yes"}
  - model_name: int/one
    litellm_params: {model: x}
    model_info: {wt_managed: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	rows := f.Rows()
	if len(rows) != 2 {
		t.Fatalf("Rows() = %v, want both rows", rows)
	}
	for _, r := range rows {
		if r.Managed {
			t.Errorf("%s: Managed = true, want false for a non-bool wt_managed", r.ID)
		}
	}
}

// stepABlock is the router_settings block Step A wrote by hand on the
// developer's machine (comment included): wt must find nothing to add to it.
const stepABlock = `router_settings:
  # Step A: one conversation stays on one deployment of a model group
  optional_pre_call_checks: [session_affinity]
  deployment_affinity_ttl_seconds: 14400
  session_affinity_repin_on_failover: true
`

// routerSettingsOf decodes f's router_settings block, failing the test when
// it is not a mapping.
func routerSettingsOf(t *testing.T, f *File) map[string]any {
	t.Helper()
	rs := mapGet(f.root(), "router_settings")
	if rs == nil || rs.Kind != yaml.MappingNode {
		t.Fatalf("router_settings is not a mapping:\n%s", enc(t, f))
	}
	return decode(t, rs)
}

// TestEnsureSettingsAddsTheAffinityBlockToAFreshConfig pins that a config.yaml
// with no router_settings gets the three session-affinity keys, with Step A's
// values, in a new block at the end of the file: without them a fresh machine
// has no sticky routing and a model group spreads one conversation over
// several deployments.
func TestEnsureSettingsAddsTheAffinityBlockToAFreshConfig(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"optional_pre_call_checks", "deployment_affinity_ttl_seconds", "session_affinity_repin_on_failover"}
	if got := routerSettingsToAdd(f.root()); !slices.Equal(got, want) {
		t.Fatalf("routerSettingsToAdd = %v, want %v", got, want)
	}
	f.EnsureSettings()
	root := f.root()
	if last := root.Content[len(root.Content)-2].Value; last != "router_settings" {
		t.Fatalf("last top-level key = %q, want router_settings:\n%s", last, enc(t, f))
	}
	rs := routerSettingsOf(t, f)
	if checks, ok := rs["optional_pre_call_checks"].([]any); !ok || !slices.Equal(checks, []any{"session_affinity"}) {
		t.Errorf("optional_pre_call_checks = %#v, want [session_affinity]", rs["optional_pre_call_checks"])
	}
	if rs["deployment_affinity_ttl_seconds"] != 14400 || rs["session_affinity_repin_on_failover"] != true || len(rs) != 3 {
		t.Errorf("router_settings = %v, want the three affinity keys only", rs)
	}
	if got := routerSettingsToAdd(f.root()); len(got) != 0 {
		t.Errorf("routerSettingsToAdd after the write = %v, want none", got)
	}
	f2, err := Open(writeConfig(t, enc(t, f)))
	if err != nil {
		t.Fatal(err)
	}
	f2.EnsureSettings()
	if f2.Changed() {
		t.Fatalf("EnsureSettings is not idempotent on the block it wrote:\n%s", enc(t, f2))
	}
}

// TestEnsureSettingsLeavesTheStepABlockAlone pins that the block already on
// the developer's machine (Step A's hand-written one, with its comment) needs
// no change, so the first sync with this code neither writes config.yaml nor
// restarts the proxy, and says nothing about it.
func TestEnsureSettingsLeavesTheStepABlockAlone(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+stepABlock))
	if err != nil {
		t.Fatal(err)
	}
	if got := routerSettingsToAdd(f.root()); len(got) != 0 {
		t.Fatalf("routerSettingsToAdd = %v, want none", got)
	}
	if w := f.settingsWarnings(); len(w) != 0 {
		t.Fatalf("settingsWarnings = %q, want none", w)
	}
	f.EnsureSettings()
	if f.Changed() {
		t.Fatalf("EnsureSettings changed the Step A block:\n%s", enc(t, f))
	}
}

// TestEnsureSettingsFillsOnlyTheMissingAffinityKeys pins that a block holding
// only the checks list gets the other two keys and keeps its list exactly as
// written: wt fills gaps, it never rewrites what the user put there.
func TestEnsureSettingsFillsOnlyTheMissingAffinityKeys(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+"router_settings:\n  optional_pre_call_checks: [session_affinity] # mine\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := routerSettingsToAdd(f.root()), []string{"deployment_affinity_ttl_seconds", "session_affinity_repin_on_failover"}; !slices.Equal(got, want) {
		t.Fatalf("routerSettingsToAdd = %v, want %v", got, want)
	}
	f.EnsureSettings()
	out := enc(t, f)
	if !strings.Contains(out, "optional_pre_call_checks: [session_affinity] # mine") {
		t.Errorf("the hand-written list was rewritten:\n%s", out)
	}
	rs := routerSettingsOf(t, f)
	if rs["deployment_affinity_ttl_seconds"] != 14400 || rs["session_affinity_repin_on_failover"] != true || len(rs) != 3 {
		t.Errorf("router_settings = %v, want the two missing keys added", rs)
	}
}

// TestEnsureSettingsKeepsAnAffinityOptOut pins the opt-out: a hand-written
// session_affinity_repin_on_failover: false and a TTL of the user's own stay
// as written, because value-enforcing them would leave no way to turn them off.
func TestEnsureSettingsKeepsAnAffinityOptOut(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+"router_settings:\n  optional_pre_call_checks: [session_affinity]\n  deployment_affinity_ttl_seconds: 3600\n  session_affinity_repin_on_failover: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	f.EnsureSettings()
	if f.Changed() {
		t.Fatalf("an affinity opt-out was rewritten:\n%s", enc(t, f))
	}
	if rs := routerSettingsOf(t, f); rs["deployment_affinity_ttl_seconds"] != 3600 || rs["session_affinity_repin_on_failover"] != false {
		t.Fatalf("router_settings = %v, want ttl 3600 and repin false kept", rs)
	}
}

// TestEnsureSettingsNeverExtendsTheChecksList pins that a checks list without
// session_affinity is the user's choice and is never extended, and that the
// sync's warnings then say affinity is off, so the choice is not silent.
func TestEnsureSettingsNeverExtendsTheChecksList(t *testing.T) {
	f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+"router_settings:\n  optional_pre_call_checks: [prompt_caching]\n"))
	if err != nil {
		t.Fatal(err)
	}
	f.EnsureSettings()
	rs := routerSettingsOf(t, f)
	if checks, ok := rs["optional_pre_call_checks"].([]any); !ok || !slices.Equal(checks, []any{"prompt_caching"}) {
		t.Fatalf("optional_pre_call_checks = %#v, want [prompt_caching] untouched", rs["optional_pre_call_checks"])
	}
	w := f.settingsWarnings()
	if len(w) != 1 || !strings.Contains(w[0], "session affinity is off") || !strings.Contains(w[0], "session_affinity") {
		t.Fatalf("settingsWarnings = %q, want one line saying session affinity is off", w)
	}
}

// TestEnsureSettingsAffinityNullAndScalarCases pins the null and scalar rules:
// an empty router_settings and an empty checks key read as absent and are
// filled, while a scalar checks value is something the user wrote and is left
// alone (and draws no affinity-off warning), and a key a merge key inside
// router_settings supplies counts as present and is not written again.
func TestEnsureSettingsAffinityNullAndScalarCases(t *testing.T) {
	t.Run("null block", func(t *testing.T) {
		f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+"router_settings:\n"))
		if err != nil {
			t.Fatal(err)
		}
		f.EnsureSettings()
		rs := routerSettingsOf(t, f)
		if len(rs) != 3 || rs["deployment_affinity_ttl_seconds"] != 14400 || rs["session_affinity_repin_on_failover"] != true {
			t.Fatalf("router_settings = %v, want the three affinity keys", rs)
		}
		if strings.Count(enc(t, f), "router_settings:") != 1 {
			t.Fatalf("duplicate block:\n%s", enc(t, f))
		}
	})
	t.Run("null checks key", func(t *testing.T) {
		f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+"router_settings:\n  optional_pre_call_checks:\n  deployment_affinity_ttl_seconds: 14400\n  session_affinity_repin_on_failover: true\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := routerSettingsToAdd(f.root()); !slices.Equal(got, []string{"optional_pre_call_checks"}) {
			t.Fatalf("routerSettingsToAdd = %v, want the checks key", got)
		}
		f.EnsureSettings()
		if checks, ok := routerSettingsOf(t, f)["optional_pre_call_checks"].([]any); !ok || !slices.Equal(checks, []any{"session_affinity"}) {
			t.Fatalf("optional_pre_call_checks = %#v, want [session_affinity]", checks)
		}
	})
	t.Run("scalar checks", func(t *testing.T) {
		f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+"router_settings:\n  optional_pre_call_checks: session_affinity\n  deployment_affinity_ttl_seconds: 14400\n  session_affinity_repin_on_failover: true\n"))
		if err != nil {
			t.Fatal(err)
		}
		f.EnsureSettings()
		if f.Changed() {
			t.Fatalf("a scalar optional_pre_call_checks was rewritten:\n%s", enc(t, f))
		}
		if w := f.settingsWarnings(); len(w) != 0 {
			t.Fatalf("settingsWarnings = %q, want none: a scalar checks value is not interpreted as a list without session_affinity", w)
		}
	})
	t.Run("merge key inside router_settings", func(t *testing.T) {
		f, err := Open(writeConfig(t, "model_list: []\n"+settledWithoutAffinity+"base: &a\n  optional_pre_call_checks: [session_affinity]\n  deployment_affinity_ttl_seconds: 14400\nrouter_settings:\n  <<: *a\n  num_retries: 2\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := routerSettingsToAdd(f.root()); !slices.Equal(got, []string{"session_affinity_repin_on_failover"}) {
			t.Fatalf("routerSettingsToAdd = %v, want only the key the merge does not supply", got)
		}
		f.EnsureSettings()
		out := enc(t, f)
		if strings.Count(out, "optional_pre_call_checks:") != 1 || strings.Count(out, "deployment_affinity_ttl_seconds:") != 1 || strings.Count(out, "session_affinity_repin_on_failover:") != 1 {
			t.Fatalf("a key supplied by the merge was written again, or the missing one was not written once:\n%s", out)
		}
		if got := routerSettingsToAdd(f.root()); len(got) != 0 {
			t.Fatalf("routerSettingsToAdd after the write = %v, want none", got)
		}
	})
}

// TestEnsureSettingsSkipsAnAliasedOrMergedRouterSettings pins that a
// router_settings shared with other keys (a whole-value alias) or supplied by
// a root merge key is never written, because a write would change the shared
// block or shadow it, and that the sync's warnings say session affinity was
// not written and why.
func TestEnsureSettingsSkipsAnAliasedOrMergedRouterSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"alias":      {"base: &r {num_retries: 2}\nrouter_settings: *r\nmodel_list: []\n" + settledWithoutAffinity, "is a YAML alias"},
		"root merge": {"base: &b\n  router_settings:\n    num_retries: 2\n<<: *b\nmodel_list: []\n" + settledWithoutAffinity, "root merge key"},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Open(writeConfig(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := routerSettingsToAdd(f.root()); len(got) != 0 {
				t.Fatalf("routerSettingsToAdd = %v, want none for a skipped block", got)
			}
			w := f.settingsWarnings()
			if len(w) != 1 || !strings.Contains(w[0], "router_settings") || !strings.Contains(w[0], "session affinity") || !strings.Contains(w[0], tc.want) {
				t.Fatalf("settingsWarnings = %q, want one naming router_settings, session affinity and %q", w, tc.want)
			}
			f.EnsureSettings()
			if f.Changed() {
				t.Fatalf("EnsureSettings wrote through a shared router_settings:\n%s", enc(t, f))
			}
		})
	}
}

// The skip reasons cancelOnDisconnectSkipped and routerSettingsSkipped give,
// and the exact warning lines settingsWarnings builds from them.
const (
	whyAlias = "is a YAML alias"
	whyMerge = "comes from a root merge key (<<)"

	affinityOffLine     = "router_settings.optional_pre_call_checks has no session_affinity, so session affinity is off and a model group may send one conversation to several deployments; add session_affinity to the list to turn it on"
	litellmSettingsDone = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
)

func cancelSkipLine(why string) string {
	return "general_settings " + why + ", so wt did not write cancel_on_disconnect: true; set it there by hand or the proxy keeps generating for abandoned non-streamed requests (#399)"
}

func affinitySkipLine(why string) string {
	return "router_settings " + why + ", so wt did not write the session affinity settings (optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds, session_affinity_repin_on_failover); set them there by hand or a model group may send one conversation to several deployments"
}

// sharedShape is one layout EnsureSettings skips: the top-level YAML that
// puts a block under its key, and the skip reason the warning names.
type sharedShape struct{ body, why string }

// sharedShapes places block (a flow mapping, or any YAML value) under key in
// every layout EnsureSettings does not write through: a whole-value alias, a
// root merge key, and a root merge key given as a list.
func sharedShapes(key, block string) map[string]sharedShape {
	return map[string]sharedShape{
		"alias":           {"base: &blk " + block + "\n" + key + ": *blk\n", whyAlias},
		"root merge":      {"base: &blk\n  " + key + ": " + block + "\n<<: *blk\n", whyMerge},
		"root merge list": {"other: &o {unrelated: 1}\nbase: &blk\n  " + key + ": " + block + "\n<<: [*o, *blk]\n", whyMerge},
	}
}

// TestAffinitySkipWarningFiresOnlyWhenTheSharedBlockLacksAKey pins #758 for
// router_settings: in every layout wt skips (a whole-value alias, a root merge
// key, a merge-key list) the skip warning fires only when the shared block, read
// as LiteLLM's loader reads it, leaves one of the three affinity keys absent or
// null; a block that sets all three (false or a TTL of the user's own included)
// draws no warning, so a sync stops printing a false alarm on every run. The
// affinity-off warning is judged on that same resolved block, so a shared
// checks list without session_affinity is still reported. wt still writes
// nothing through the shared block in any case.
func TestAffinitySkipWarningFiresOnlyWhenTheSharedBlockLacksAKey(t *testing.T) {
	const all = "{optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 14400, session_affinity_repin_on_failover: true}"
	skip := func(why string) []string { return []string{affinitySkipLine(why)} }
	none := func(string) []string { return nil }
	for name, tc := range map[string]struct {
		prefix, block string
		want          func(why string) []string
	}{
		"all three keys":           {"", all, none},
		"own ttl and repin false":  {"", "{optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 3600, session_affinity_repin_on_failover: false}", none},
		"keys from a merge inside": {"keys: &k {optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 14400}\n", "{<<: *k, session_affinity_repin_on_failover: true}", none},
		"no checks":                {"", "{deployment_affinity_ttl_seconds: 14400, session_affinity_repin_on_failover: true}", skip},
		"no ttl":                   {"", "{optional_pre_call_checks: [session_affinity], session_affinity_repin_on_failover: true}", skip},
		"no repin":                 {"", "{optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 14400}", skip},
		"none of them":             {"", "{num_retries: 2}", skip},
		"null checks":              {"", "{optional_pre_call_checks: null, deployment_affinity_ttl_seconds: 14400, session_affinity_repin_on_failover: true}", skip},
		"null ttl":                 {"", "{optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: ~, session_affinity_repin_on_failover: true}", skip},
		"null repin":               {"", "{optional_pre_call_checks: [session_affinity], deployment_affinity_ttl_seconds: 14400, session_affinity_repin_on_failover: null}", skip},
		"checks without session_affinity": {"", "{optional_pre_call_checks: [prompt_caching], deployment_affinity_ttl_seconds: 14400, session_affinity_repin_on_failover: true}",
			func(string) []string { return []string{affinityOffLine} }},
		"missing keys and checks without session_affinity": {"", "{optional_pre_call_checks: [prompt_caching]}",
			func(why string) []string { return []string{affinitySkipLine(why), affinityOffLine} }},
	} {
		for shape, s := range sharedShapes("router_settings", tc.block) {
			t.Run(name+"/"+shape, func(t *testing.T) {
				f, err := Open(writeConfig(t, tc.prefix+s.body+"model_list: []\n"+settledWithoutAffinity))
				if err != nil {
					t.Fatal(err)
				}
				if got, want := f.settingsWarnings(), tc.want(s.why); !slices.Equal(got, want) {
					t.Fatalf("settingsWarnings =\n%q\nwant\n%q", got, want)
				}
				if got := routerSettingsToAdd(f.root()); len(got) != 0 {
					t.Fatalf("routerSettingsToAdd = %v, want none for a skipped block", got)
				}
				f.EnsureSettings()
				if f.Changed() {
					t.Fatalf("EnsureSettings wrote through a shared router_settings:\n%s", enc(t, f))
				}
			})
		}
	}
}

// TestCancelSkipWarningFiresOnlyWhenTheSharedBlockLacksTheKey pins the same
// rule for general_settings (#758), so the two skip warnings stay one pattern:
// in every layout wt skips, the cancel_on_disconnect warning fires only when
// the shared block, resolved as LiteLLM's loader resolves it, leaves the key
// absent or null. A hand-written value, false included, is the opt-out and
// draws no warning, and wt still writes nothing through the shared block.
func TestCancelSkipWarningFiresOnlyWhenTheSharedBlockLacksTheKey(t *testing.T) {
	const db = "database_url: postgresql://u@localhost/db, store_model_in_db: true"
	for name, tc := range map[string]struct {
		prefix, block string
		warn          bool
	}{
		"true":                    {"", "{" + db + ", cancel_on_disconnect: true}", false},
		"false":                   {"", "{" + db + ", cancel_on_disconnect: false}", false},
		"another value":           {"", "{" + db + ", cancel_on_disconnect: sometimes}", false},
		"key from a merge inside": {"keys: &k {cancel_on_disconnect: true}\n", "{<<: *k, " + db + "}", false},
		"absent":                  {"", "{" + db + "}", true},
		"null":                    {"", "{" + db + ", cancel_on_disconnect: null}", true},
	} {
		for shape, s := range sharedShapes("general_settings", tc.block) {
			t.Run(name+"/"+shape, func(t *testing.T) {
				f, err := Open(writeConfig(t, tc.prefix+s.body+"model_list: []\n"+litellmSettingsDone+affinitySettings))
				if err != nil {
					t.Fatal(err)
				}
				var want []string
				if tc.warn {
					want = []string{cancelSkipLine(s.why)}
				}
				if got := f.settingsWarnings(); !slices.Equal(got, want) {
					t.Fatalf("settingsWarnings =\n%q\nwant\n%q", got, want)
				}
				f.EnsureSettings()
				if f.Changed() {
					t.Fatalf("EnsureSettings wrote through a shared general_settings:\n%s", enc(t, f))
				}
			})
		}
	}
}

// TestSkipWarningsFailClosedOnAnUnreadableSharedBlock pins that a skipped
// router_settings or general_settings that does not resolve to a mapping (an
// alias to null, a scalar or a list, a root merge supplying a scalar, or an
// alias node whose anchor is gone) keeps the skip warning exactly as before
// #758: when wt cannot read the shared block it cannot say the protection is
// on, so it warns rather than going quiet.
func TestSkipWarningsFailClosedOnAnUnreadableSharedBlock(t *testing.T) {
	for _, b := range []struct {
		key, rest string
		line      func(string) string
	}{
		{"router_settings", settledWithoutAffinity, affinitySkipLine},
		{"general_settings", litellmSettingsDone + affinitySettings, cancelSkipLine},
	} {
		for name, tc := range map[string]struct {
			body, why string
			dangle    func(root *yaml.Node) // breaks an alias after parsing, or nil
		}{
			"alias to null":          {"base: &blk\n" + b.key + ": *blk\n", whyAlias, nil},
			"alias to a scalar":      {"base: &blk 5\n" + b.key + ": *blk\n", whyAlias, nil},
			"alias to a list":        {"base: &blk [a, b]\n" + b.key + ": *blk\n", whyAlias, nil},
			"root merge of a scalar": {"base: &blk {" + b.key + ": 5}\n<<: *blk\n", whyMerge, nil},
			"root merge of a list":   {"base: &blk {" + b.key + ": [a]}\n<<: *blk\n", whyMerge, nil},
			"dangling whole-value alias": {"base: &blk {x: 1}\n" + b.key + ": *blk\n", whyAlias, func(root *yaml.Node) {
				mapGet(root, b.key).Alias = nil
			}},
			"dangling alias in a root merge": {"inner: &in {x: 1}\nbase: &blk\n  " + b.key + ": *in\n<<: *blk\n", whyMerge, func(root *yaml.Node) {
				mapGet(mapGet(root, "base"), b.key).Alias = nil
			}},
		} {
			t.Run(b.key+"/"+name, func(t *testing.T) {
				f, err := Open(writeConfig(t, tc.body+"model_list: []\n"+b.rest))
				if err != nil {
					t.Fatal(err)
				}
				if tc.dangle != nil {
					tc.dangle(f.root())
				}
				if got, want := f.settingsWarnings(), []string{b.line(tc.why)}; !slices.Equal(got, want) {
					t.Fatalf("settingsWarnings =\n%q\nwant\n%q", got, want)
				}
				if tc.dangle != nil {
					return // the hand-broken alias does not encode
				}
				f.EnsureSettings()
				if f.Changed() {
					t.Fatalf("EnsureSettings wrote through a shared %s:\n%s", b.key, enc(t, f))
				}
			})
		}
	}
}

// TestDBRoutesOffWarningIsUnchangedForASharedGeneralSettings pins that #758
// quiets only the cancel_on_disconnect warning: a shared general_settings that
// sets cancel_on_disconnect but not store_model_in_db still gets the database
// routes warning (dbRoutesOffWarning already reads the resolved block), and
// one that sets both gets neither.
func TestDBRoutesOffWarningIsUnchangedForASharedGeneralSettings(t *testing.T) {
	for _, tc := range []struct {
		name, block string
		wantDBOff   bool
	}{
		{"cancel only", "{cancel_on_disconnect: true}", true},
		{"cancel and store", "{cancel_on_disconnect: true, store_model_in_db: true}", false},
	} {
		for shape, s := range sharedShapes("general_settings", tc.block) {
			t.Run(tc.name+"/"+shape, func(t *testing.T) {
				f, err := Open(writeConfig(t, s.body+"model_list: []\n"+litellmSettingsDone+affinitySettings))
				if err != nil {
					t.Fatal(err)
				}
				if got := f.settingsWarnings(); len(got) != 0 {
					t.Fatalf("settingsWarnings = %q, want none: the shared block sets cancel_on_disconnect", got)
				}
				want := ""
				if tc.wantDBOff {
					want = "general_settings " + s.why + ", so wt did not write store_model_in_db: true; database routes are off and local routes stay in config.yaml with a restart per change"
				}
				if got := f.dbRoutesOffWarning(); got != want {
					t.Fatalf("dbRoutesOffWarning = %q, want %q", got, want)
				}
			})
		}
	}
}

// TestSaveKeepsAHandWrittenRouterSettingsMeaning pins that a hand-written
// router_settings (comments, an anchor, a key wt does not name) comes back
// from EnsureSettings, Save and a reopen with every key, value, comment and
// anchor intact, in its own order, and nothing added: wt must never lose or
// reorder what the user wrote there.
func TestSaveKeepsAHandWrittenRouterSettingsMeaning(t *testing.T) {
	const block = `router_settings:
  # retries first
  num_retries: 2
  optional_pre_call_checks: &checks [session_affinity] # sticky
  deployment_affinity_ttl_seconds: 14400
  session_affinity_repin_on_failover: true
other_checks: *checks
`
	p := writeConfig(t, "model_list: []\n"+settledWithoutAffinity+block)
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	f.EnsureSettings()
	if f.Changed() {
		t.Fatalf("EnsureSettings changed a complete hand-written block:\n%s", enc(t, f))
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	g, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	out := readFile(t, p)
	for _, want := range []string{"# retries first", "&checks", "# sticky", "other_checks: *checks"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	rs := mapGet(g.root(), "router_settings")
	var keys []string
	for i := 0; i+1 < len(rs.Content); i += 2 {
		keys = append(keys, rs.Content[i].Value)
	}
	if want := []string{"num_retries", "optional_pre_call_checks", "deployment_affinity_ttl_seconds", "session_affinity_repin_on_failover"}; !slices.Equal(keys, want) {
		t.Fatalf("router_settings keys = %v, want %v", keys, want)
	}
	m := routerSettingsOf(t, g)
	if m["num_retries"] != 2 || m["deployment_affinity_ttl_seconds"] != 14400 || m["session_affinity_repin_on_failover"] != true {
		t.Fatalf("router_settings = %v", m)
	}
	if checks, ok := m["optional_pre_call_checks"].([]any); !ok || !slices.Equal(checks, []any{"session_affinity"}) {
		t.Fatalf("optional_pre_call_checks = %#v", m["optional_pre_call_checks"])
	}
}
