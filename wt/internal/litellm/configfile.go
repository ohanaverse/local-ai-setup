package litellm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

// lockPollInterval is how long an interruptible (context-bounded) WithLock
// wait sleeps between non-blocking flock attempts. Short enough that a
// cancel is noticed promptly, long enough not to spin.
const lockPollInterval = 25 * time.Millisecond

var (
	// ErrMissing: config.yaml does not exist (LiteLLM not set up).
	ErrMissing = errors.New("LiteLLM config not found")
	// ErrInvalid: config.yaml exists but is not a YAML mapping, or its
	// model_list is not a list. wt refuses to write such a file.
	ErrInvalid = errors.New("LiteLLM config is invalid")
	// ErrRegistryRedirected: the environment redirected the registry but
	// nothing named config.yaml. No route write or dry run goes ahead (see
	// checkRegistryPairing). modelman's wt_bridge matches this text to tell
	// the refusal from a failed sync, so change both together.
	ErrRegistryRedirected = errors.New("LiteLLM routes not touched")
)

// DefaultPath resolves config.yaml lazily so env overrides work in tests:
// WT_LITELLM_CONFIG, then legacy MODELMAN_LITELLM_CONFIG, then
// ~/.config/litellm/config.yaml.
func DefaultPath() string {
	if p, ok := namedPath(); ok {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "litellm", "config.yaml")
}

// namedPath is the config.yaml the environment names, if it names one.
func namedPath() (string, bool) {
	for _, k := range []string{"WT_LITELLM_CONFIG", "MODELMAN_LITELLM_CONFIG"} {
		if v := os.Getenv(k); v != "" {
			if exp, err := config.ExpandHome(v); err == nil {
				return exp, true
			}
			return v, true
		}
	}
	return "", false
}

// checkRegistryPairing refuses the one combination no caller means: a
// registry the environment redirected (config.RegistryRedirected) with a
// config.yaml nobody named. The two paths resolve independently — the
// registry follows MODELMAN_REGISTRY and XDG_CONFIG_HOME, config.yaml only
// WT_LITELLM_CONFIG — so redirecting the registry alone reconciles the
// developer's real proxy config against a scratch registry: every marked
// route that registry lacks is removed and the live proxy restarted. Naming
// config.yaml (the environment, or Options.Path) says which proxy config the
// registry belongs to and is always honored, the default file included.
func checkRegistryPairing(o Options) error {
	if o.Path != "" {
		return nil
	}
	if _, ok := namedPath(); ok || !config.RegistryRedirected() {
		return nil
	}
	return fmt.Errorf("%w: the registry is %s but config.yaml is the default %s — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to",
		ErrRegistryRedirected, config.RegistryPath(), DefaultPath())
}

// enforcedSettings are value-enforced under litellm_settings on every write.
var enforcedSettings = []string{
	"drop_params",
	"use_chat_completions_url_for_anthropic_messages",
}

// wtParamKeys are the litellm_params keys wt itself derives from the registry
// and provider policy (entry.go's BuildEntry): model unconditionally, api_base
// only when the provider supplies a base URL, and api_key from whichever the
// policy names — its secret ref (resolving the provider's credential) or its
// literal key. A row replacement never carries these over from the old row:
// the registry and provider policy own them, and a change there must reach
// config.yaml. Every other key on the old row is user-authored (a hand-written
// timeout, rate limit, ...) and is carried over when the new row lacks it — the
// presence-based keys additional_drop_params and use_chat_completions_api
// (EnsureSettings writes them only when absent) fall out of this rule.
var wtParamKeys = map[string]bool{"model": true, "api_base": true, "api_key": true}

// droppedOllamaChatParams are dropped by default on every fresh ollama_chat/
// deployment via additional_drop_params: reasoning_effort crashes litellm's
// ollama_chat responses bridge for codex (see
// docs/wt-agents/litellm-troubleshooting.md); frequency_penalty and
// presence_penalty map to ollama's repeat_penalty and can produce a value
// ollama's sampler rejects ("must be finite and greater than 0") for some
// models even when the caller sends 0 — copilot CLI always sends both.
// Cost: litellm has no per-model way to drop a param only where it crashes,
// so any client-supplied value for these three is silently ignored for
// every ollama_chat/ model, including ones that would have handled it fine.
var droppedOllamaChatParams = []string{"reasoning_effort", "frequency_penalty", "presence_penalty"}

var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// File is an open, editable LiteLLM config.yaml.
type File struct {
	path   string
	doc    yaml.Node
	before []byte
}

// Open parses path. It returns ErrMissing or ErrInvalid without touching
// the file.
func Open(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", ErrMissing, path)
	}
	if err != nil {
		return nil, err
	}
	f := &File{path: path}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&f.doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	// Saving would silently drop every document after the first (ruamel
	// refuses such a stream too), so refuse to open it.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s: expected a single document in the stream", ErrInvalid, path)
	}
	if f.doc.Kind != yaml.DocumentNode || len(f.doc.Content) == 0 || f.doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: %s is not a mapping", ErrInvalid, path)
	}
	if f.before, err = f.encode(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return f, nil
}

func (f *File) root() *yaml.Node { return f.doc.Content[0] }

// encode serializes the document with a 2-space indent and no line folding.
func (f *File) encode() ([]byte, error) {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(&f.doc); err != nil {
		return nil, fmt.Errorf("encode %s: %w", f.path, err)
	}
	if err := e.Close(); err != nil {
		return nil, fmt.Errorf("encode %s: %w", f.path, err)
	}
	return b.Bytes(), nil
}

// Changed reports whether the document differs from what Open parsed
// (compared through the same encoder, so pure re-indentation is not a change).
// If the document cannot be encoded it reports true (the safe direction: the
// caller proceeds to Save, which refuses with the encode error and leaves the
// file untouched, rather than skipping and hiding the failure).
func (f *File) Changed() bool {
	cur, err := f.encode()
	return err != nil || !bytes.Equal(f.before, cur)
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Overwriting a value keeps the old node's comments (an inline
			// "# note" after the value would otherwise vanish).
			old := m.Content[i+1]
			val.HeadComment, val.LineComment, val.FootComment = old.HeadComment, old.LineComment, old.FootComment
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

// boolNode encodes a literal bool, which always succeeds.
func boolNode(v bool) *yaml.Node {
	n, _ := toNode(v)
	return n
}

// stringSeq builds a YAML sequence of string scalars. Each value is a
// literal Go string, so toNode always succeeds.
func stringSeq(values []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		n, _ := toNode(v)
		seq.Content = append(seq.Content, n)
	}
	return seq
}

func isTrue(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!bool" && n.Value == "true"
}

func rowName(row *yaml.Node) string {
	if n := mapGet(row, "model_name"); n != nil {
		return n.Value
	}
	return ""
}

// ManagedKey is the model_info key wt stamps on every row it writes (#179).
// Sync removes only rows wt owns; the marker is how a row says so.
const ManagedKey = "wt_managed"

// IsManaged reports whether row carries wt's ownership marker
// (model_info.wt_managed: true; any other value counts as hand-written).
func IsManaged(row *yaml.Node) bool {
	return isTrue(mapGet(mapGet(row, "model_info"), ManagedKey))
}

// RowInfo is one model_list row's name and whether wt's marker is on it.
type RowInfo struct {
	ID      string
	Managed bool
}

// modelListSeq returns the model_list sequence, or nil when the key is absent,
// null, or its value is not a sequence. It never mutates: modelList is the only
// accessor allowed to create the key, and a read-only caller that created it
// would flip File.Changed, making a no-op sync write config.yaml and restart
// the proxy.
func (f *File) modelListSeq() *yaml.Node {
	ml := mapGet(f.root(), "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return nil
	}
	return ml
}

// Rows returns every named model_list mapping row, in file order.
func (f *File) Rows() []RowInfo {
	ml := f.modelListSeq()
	if ml == nil {
		return nil
	}
	var out []RowInfo
	for _, row := range ml.Content {
		if id := rowName(row); row.Kind == yaml.MappingNode && id != "" {
			out = append(out, RowInfo{ID: id, Managed: IsManaged(row)})
		}
	}
	return out
}

// hasUnmarkedRow reports whether any mapping row named id lacks wt's marker:
// a hand-written row of that name, which a discovered route never replaces.
func (f *File) hasUnmarkedRow(id string) bool {
	for _, r := range f.Rows() {
		if r.ID == id && !r.Managed {
			return true
		}
	}
	return false
}

// row returns the first mapping row named id, or nil.
func (f *File) row(id string) *yaml.Node {
	ml := f.modelListSeq()
	if ml == nil {
		return nil
	}
	for _, r := range ml.Content {
		if r.Kind == yaml.MappingNode && rowName(r) == id {
			return r
		}
	}
	return nil
}

// carryUserParams copies the user-authored litellm_params keys — anything wt
// does not itself set (see wtParamKeys) — from old into row when row lacks
// them: what SetRow does on replace, factored out so sync can compare a
// rebuilt row with the one on disk exactly as SetRow would write it. Adoption
// and rewrites therefore keep hand-written params (a timeout, say) instead of
// silently dropping them.
func carryUserParams(old, row *yaml.Node) {
	oldParams, newParams := mapGet(old, "litellm_params"), mapGet(row, "litellm_params")
	if oldParams == nil || newParams == nil {
		return
	}
	if oldParams.Kind != yaml.MappingNode || newParams.Kind != yaml.MappingNode {
		// A hand-edited litellm_params that is not a mapping ([foo, bar], say)
		// has no key/value pairs to read: pairing off its Content would invent
		// param names from its elements. Leave the row to be rewritten from the
		// registry rather than derive one from a node we cannot interpret.
		return
	}
	for i := 0; i+1 < len(oldParams.Content); i += 2 {
		k := oldParams.Content[i]
		if k.Kind != yaml.ScalarNode {
			// A complex key (? {weird: key}: 1) is a mapping node whose Value
			// is "": carried as-is it would inject a nameless param.
			continue
		}
		if wtParamKeys[k.Value] {
			continue
		}
		if v := oldParams.Content[i+1]; mapGet(newParams, k.Value) == nil {
			mapSet(newParams, k.Value, v)
		}
	}
}

// checkModelList reports a model_list whose value is present but is not a
// sequence. An explicit null reads as absent, the same shape modelList creates,
// so a bare "model_list:" is accepted here exactly as there. It never creates
// the key: unlike modelList it is safe to call from a read-only path such as a
// dry run, where creating it would flip File.Changed and make a no-op sync
// write config.yaml and restart the proxy.
func (f *File) checkModelList() error {
	ml := mapGet(f.root(), "model_list")
	if ml != nil && !isNull(ml) && ml.Kind != yaml.SequenceNode {
		return fmt.Errorf("%w: model_list is not a list in %s", ErrInvalid, f.path)
	}
	return nil
}

// modelList returns the model_list sequence, creating it when absent or null.
// A non-list value is ErrInvalid: never edit what we do not understand.
func (f *File) modelList() (*yaml.Node, error) {
	if err := f.checkModelList(); err != nil {
		return nil, err
	}
	if ml := f.modelListSeq(); ml != nil {
		return ml, nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapSet(f.root(), "model_list", seq)
	return seq, nil
}

// RoutedIDs returns every model_list row's model_name, in file order — the
// ids of Rows, kept for tests that only compare names.
func (f *File) RoutedIDs() []string {
	var ids []string
	for _, r := range f.Rows() {
		ids = append(ids, r.ID)
	}
	return ids
}

// SetRow adds the row keyed by id, or replaces the existing one in place,
// carrying over the old row's user-authored params the new row lacks
// (wtParamKeys — model/api_base/api_key — are wt's and never carried).
// LiteLLM load-balances across rows sharing a model_name, so a hand-edited
// file with duplicates would keep routing to the stale copies: the first
// mapping row is replaced and every later duplicate is dropped (RemoveRow
// likewise removes all of them).
func (f *File) SetRow(id string, row *yaml.Node) error {
	ml, err := f.modelList()
	if err != nil {
		return err
	}
	first := -1
	kept := ml.Content[:0]
	for _, old := range ml.Content {
		if old.Kind != yaml.MappingNode || rowName(old) != id {
			kept = append(kept, old)
			continue
		}
		if first >= 0 {
			continue // duplicate of a row already replaced
		}
		carryUserParams(old, row)
		row.HeadComment, row.LineComment, row.FootComment = old.HeadComment, old.LineComment, old.FootComment
		first = len(kept)
		kept = append(kept, row)
	}
	ml.Content = kept
	if first < 0 {
		ml.Content = append(ml.Content, row)
	}
	return nil
}

// RemoveRow deletes the rows keyed by id (no-op when absent). Rows that are
// not mappings are never ours and are left in place.
func (f *File) RemoveRow(id string) bool {
	return f.removeRows(id, func(*yaml.Node) bool { return true })
}

// RemoveMarkedRows deletes the rows keyed by id that carry wt's marker,
// leaving any unmarked (hand-written) row of the same name in place.
func (f *File) RemoveMarkedRows(id string) bool {
	return f.removeRows(id, IsManaged)
}

func (f *File) removeRows(id string, drop func(*yaml.Node) bool) (removed bool) {
	ml := f.modelListSeq()
	if ml == nil {
		return false
	}
	kept := ml.Content[:0]
	for _, row := range ml.Content {
		if row.Kind == yaml.MappingNode && rowName(row) == id && drop(row) {
			removed = true
			continue
		}
		kept = append(kept, row)
	}
	ml.Content = kept
	return removed
}

func isLoopback(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	u, err := url.Parse(n.Value)
	if err != nil || u.Host == "" {
		// Schemeless input ("localhost:11434", "127.0.0.1:8000"): url.Parse
		// either reads the part before the colon as a scheme and everything
		// after as opaque (Host/Hostname() come back empty), or — when that
		// prefix isn't a valid scheme, e.g. an IP literal — rejects the
		// whole value outright ("first path segment in URL cannot contain
		// colon"). Reparse with a "//" prefix so it resolves as a host
		// instead.
		u, err = url.Parse("//" + n.Value)
		if err != nil {
			return false
		}
	}
	return loopbackHosts[strings.ToLower(u.Hostname())]
}

// mergeDepth bounds how many "<<" merges mergedGet follows from one mapping. A
// real config nests one or two; the bound only keeps an anchor that merges
// itself from recursing without end.
const mergeDepth = 8

// mergedGet is mapGet that also looks through the mapping's YAML merge keys
// ("<<: *defaults", or a list of them), nearest first: a key the mapping names
// itself wins, then each merged mapping in order. That is the value the YAML
// loader LiteLLM reads config.yaml with hands it, so a key supplied by a merge
// is present as far as the proxy is concerned even though the row does not
// spell it.
func mergedGet(m *yaml.Node, key string) *yaml.Node {
	return mergedGetDepth(m, key, mergeDepth)
}

func mergedGetDepth(m *yaml.Node, key string, depth int) *yaml.Node {
	if v := mapGet(m, key); v != nil || m == nil || m.Kind != yaml.MappingNode || depth == 0 {
		return v
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind != yaml.ScalarNode || k.Tag != "!!merge" {
			continue
		}
		sources := []*yaml.Node{m.Content[i+1]}
		if sources[0].Kind == yaml.SequenceNode {
			sources = sources[0].Content
		}
		for _, s := range sources {
			if s.Kind == yaml.AliasNode {
				s = s.Alias
			}
			if v := mergedGetDepth(s, key, depth-1); v != nil {
				return v
			}
		}
	}
	return nil
}

// EnsureOllamaAPIBase gives every ollama row with no api_base the address
// base, and returns the names of the rows it changed (#202), each once. A row
// is an ollama one when its model has the "ollama/" or "ollama_chat/" prefix,
// and has no api_base when the key is absent, null or empty. Both are read
// through YAML merge keys (mergedGet): a row that takes its api_base from
// "<<: *defaults" names an address, and writing one beside the merge would
// override it.
//
// It exists because of what LiteLLM does with such a row: at proxy startup,
// for each model that names ollama and has no api_base, it runs `ollama serve`
// itself. On a machine where ollama is already running, that second server
// binds 127.0.0.1:11434 beside the first one's wildcard socket, and from then
// on a client reaches one or the other depending on whether "localhost"
// resolved to IPv4 or IPv6. A model gets loaded in both; wt stops it in the
// one it can see and reports success while the other copy stays resident.
//
// So this repairs hand-written rows too — the one field, and only when it is
// empty: a row that names an address of its own is the user's choice and is
// never touched, and a repaired row stays unmarked, still the user's. base is
// OllamaAPIBase's answer, which is never empty; given "", nothing is changed.
// A row with no model_name is repaired like any other and named by rowLabel.
// An empty value that carries a YAML anchor (`api_base: &base ""`) is the one
// exception to the repair: see the note at the write.
func (f *File) EnsureOllamaAPIBase(base string) []string {
	ml := f.modelListSeq()
	if base == "" || ml == nil {
		return nil
	}
	var names []string
	for _, row := range ml.Content {
		p := mapGet(row, "litellm_params")
		if p == nil || p.Kind != yaml.MappingNode {
			continue
		}
		model := mergedGet(p, "model")
		if model == nil || model.Kind != yaml.ScalarNode {
			continue
		}
		if !strings.HasPrefix(model.Value, "ollama/") && !strings.HasPrefix(model.Value, "ollama_chat/") {
			continue
		}
		if b := mergedGet(p, "api_base"); b != nil && !isNull(b) && !(b.Kind == yaml.ScalarNode && strings.TrimSpace(b.Value) == "") {
			continue
		}
		// The row's own empty value may carry an anchor that other rows
		// alias. Replacing the node would drop the anchor and leave those
		// aliases pointing at nothing — a file that no longer parses — and
		// keeping it would hand the ollama address to every row that
		// aliases it. Left as written; a null one is in the sync's warnings.
		if own := mapGet(p, "api_base"); own != nil && own.Anchor != "" {
			continue
		}
		mapSet(p, "api_base", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: base})
		// Two rows may share a model_name; the report names it once, as
		// planSync does for a removal.
		if name := rowLabel(row, model.Value); !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// rowLabel is what a report calls a row: its model_name, or — for a row that
// has none — a label saying so and naming its model. "" would print as
// ": api_base set" and reach `sync --json` as an empty id (#206); the bare
// model value would be mistaken for the row of that name.
func rowLabel(row *yaml.Node, model string) string {
	if name := rowName(row); name != "" {
		return name
	}
	return "(no model_name: " + model + ")"
}

// ollamaServeWarnings names every row LiteLLM starts its own `ollama serve`
// for at proxy startup, in LiteLLM's own terms (proxy_server.py): the word
// "ollama" anywhere in litellm_params.model and an api_base that is None —
// absent or null, read through merge keys as LiteLLM's loader resolves them;
// an empty string is not None. Called after EnsureOllamaAPIBase, what is left
// is the rows that repair does not reach: ones whose model is not an ollama/
// or ollama_chat/ one (openai/ollama-proxy), which wt cannot give an address
// because the ollama one would be wrong for them (#206). Sync reports these;
// it never changes them.
//
// An api_base written `os.environ/VAR` is resolved by LiteLLM before that
// test, and an unset variable resolves to None (#211) — so the row is the
// same trigger, though it looks as if it names an address. env says whether
// VAR is set for the proxy (ProxyEnv: the LaunchAgent's environment, not
// wt's). Such a row is reported, naming the variable and where wt looked, and
// never rewritten, an ollama/ row included: a variable may point at another
// server on purpose, and the ollama address written over it would hide that.
//
// A whole value spelled as a YAML alias — `litellm_params: *params`,
// `model: *model` or `api_base: *base` — is resolved the same way LiteLLM's
// loader resolves it, so aliasing cannot hide a row from the scan.
func (f *File) ollamaServeWarnings(env ProxyEnv) []string {
	ml := f.modelListSeq()
	if ml == nil {
		return nil
	}
	aliasGet := func(n *yaml.Node) *yaml.Node {
		if n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
			return n.Alias
		}
		return n
	}
	var out []string
	for _, row := range ml.Content {
		p := aliasGet(mapGet(row, "litellm_params"))
		if p == nil || p.Kind != yaml.MappingNode {
			continue
		}
		model := aliasGet(mergedGet(p, "model"))
		if model == nil || model.Kind != yaml.ScalarNode || !strings.Contains(model.Value, "ollama") {
			continue
		}
		b := aliasGet(mergedGet(p, "api_base"))
		if b == nil || isNull(b) {
			out = append(out, fmt.Sprintf(`row %q (model %s) has no api_base: LiteLLM starts its own "ollama serve" for it at proxy startup; give the row an api_base`, rowLabel(row, model.Value), model.Value))
			continue
		}
		if b.Kind == yaml.ScalarNode && strings.HasPrefix(b.Value, "os.environ/") {
			// The variable LiteLLM looks up is the value with every
			// "os.environ/" dropped, not only the leading one (get_secret:
			// secret_name.replace("os.environ/", "")).
			name := strings.ReplaceAll(b.Value, "os.environ/", "")
			switch {
			case name != "" && env.IsSet(name):
				// Set for the proxy: the row names an address.
			case strings.TrimSpace(name) == "":
				// The prefix with no variable after it, or only spaces. No
				// variable is named "" and LiteLLM gets None, so it is the
				// same trigger — but there is no name to report as unset, and
				// the sentence for a named variable would print three holes
				// (#218).
				out = append(out, fmt.Sprintf(`row %q (model %s) has api_base os.environ/ with no variable name: LiteLLM resolves that to None and starts its own "ollama serve" for it at proxy startup; give the row an address or spell os.environ/<VAR>`,
					rowLabel(row, model.Value), model.Value))
			default:
				out = append(out, fmt.Sprintf(`row %q (model %s) has api_base %s, and %s is not set in %s: LiteLLM starts its own "ollama serve" for it at proxy startup; set %s for the proxy or give the row an address`,
					rowLabel(row, model.Value), model.Value, b.Value, name, env.Source, name))
			}
		}
	}
	return out
}

// rowDigests maps each model_name to the encoded text of its rows, in order.
// Two digests taken around a write say which names' rows it changed, which
// File.Changed — a whole-document comparison — cannot. applyPlanned takes them
// only when the plan adds a row, the only outcome Written is set on.
func (f *File) rowDigests() map[string]string {
	ml := f.modelListSeq()
	if ml == nil {
		return nil
	}
	out := map[string]string{}
	for _, row := range ml.Content {
		if row.Kind != yaml.MappingNode {
			continue
		}
		b, err := yaml.Marshal(row)
		if err != nil {
			// Unencodable rows cannot be compared; Save refuses the document.
			b = []byte("unencodable")
		}
		out[rowName(row)] += string(b) + "\x00"
	}
	return out
}

// EnsureSettings applies the launcher-required LiteLLM settings: two
// value-enforced litellm_settings keys, plus presence-based per-row params
// (additional_drop_params on ollama_chat/ rows, use_chat_completions_api on
// loopback openai/ rows). Tolerates hand-edited degenerate shapes.
func (f *File) EnsureSettings() {
	root := f.root()
	ls := mapGet(root, "litellm_settings")
	if ls == nil || isNull(ls) {
		ls, _ = mapping(nil) // nil pairs never fail
		mapSet(root, "litellm_settings", ls)
	}
	if ls.Kind == yaml.MappingNode {
		for _, k := range enforcedSettings {
			if !isTrue(mapGet(ls, k)) {
				mapSet(ls, k, boolNode(true))
			}
		}
	}
	ml := f.modelListSeq()
	if ml == nil {
		return
	}
	for _, row := range ml.Content {
		params := mapGet(row, "litellm_params")
		if params == nil || params.Kind != yaml.MappingNode {
			continue
		}
		model := mapGet(params, "model")
		if model == nil || model.Kind != yaml.ScalarNode {
			continue
		}
		switch {
		case strings.HasPrefix(model.Value, "ollama_chat/") && mapGet(params, "additional_drop_params") == nil:
			mapSet(params, "additional_drop_params", stringSeq(droppedOllamaChatParams))
		case strings.HasPrefix(model.Value, "openai/") && mapGet(params, "use_chat_completions_api") == nil && isLoopback(mapGet(params, "api_base")):
			mapSet(params, "use_chat_completions_api", boolNode(true))
		}
	}
}

// Save writes the document atomically (temp file + rename) keeping the
// original file's permission bits: the file holds API keys.
func (f *File) Save() error {
	out, err := f.encode()
	if err != nil {
		return err
	}
	// Write through a symlinked config.yaml (e.g. into a dotfiles repo): the
	// temp file and rename target the real file, so the link survives.
	target := f.path
	if resolved, err := filepath.EvalSymlinks(f.path); err == nil {
		target = resolved
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".litellm-config-*.yaml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// WithLock serializes read-modify-write cycles on path across processes with
// an flock on path+".lock".
//
// The wait honors ctx: flock offers no timeout, so the lock is taken
// non-blockingly and retried, letting a caller with a bounded context (the
// lifecycle route hook's settling bounce, Ctrl+C on a start/stop) abandon a
// contended lock instead of hanging past its own deadline. A nil ctx waits
// without bound. Note the lock is released by fn returning — a cancelled ctx
// never leaves the flock held.
func WithLock(ctx context.Context, path string, fn func() error) error {
	// Fail as Open would before creating a lock file: no LiteLLM setup must
	// surface as ErrMissing (not a raw OS error) and leave nothing behind.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrMissing, path)
	}
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	for {
		err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if ctx == nil {
			// No deadline to honor: fall back to a blocking wait rather than
			// spinning on a lock this process may hold for a while.
			if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
				return err
			}
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s.lock: %w", path, ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}
