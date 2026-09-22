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
)

// DefaultPath resolves config.yaml lazily so env overrides work in tests:
// WT_LITELLM_CONFIG, then legacy MODELMAN_LITELLM_CONFIG, then
// ~/.config/litellm/config.yaml.
func DefaultPath() string {
	for _, k := range []string{"WT_LITELLM_CONFIG", "MODELMAN_LITELLM_CONFIG"} {
		if v := os.Getenv(k); v != "" {
			if exp, err := config.ExpandHome(v); err == nil {
				return exp
			}
			return v
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "litellm", "config.yaml")
}

// enforcedSettings are value-enforced under litellm_settings on every write.
var enforcedSettings = []string{
	"drop_params",
	"use_chat_completions_url_for_anthropic_messages",
}

// preservedParamKeys survive a row replacement when the new row lacks them:
// they are presence-based (an existing value of any kind marks the row
// user-managed).
var preservedParamKeys = []string{"additional_drop_params", "use_chat_completions_api"}

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

func isTrue(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!bool" && n.Value == "true"
}

func rowName(row *yaml.Node) string {
	if n := mapGet(row, "model_name"); n != nil {
		return n.Value
	}
	return ""
}

// modelList returns the model_list sequence, creating it when absent or null.
// A non-list value is ErrInvalid: never edit what we do not understand.
func (f *File) modelList() (*yaml.Node, error) {
	ml := mapGet(f.root(), "model_list")
	switch {
	case ml == nil || isNull(ml):
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		mapSet(f.root(), "model_list", seq)
		return seq, nil
	case ml.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("%w: model_list is not a list in %s", ErrInvalid, f.path)
	}
	return ml, nil
}

// RoutedIDs returns every model_list row's model_name, in file order.
func (f *File) RoutedIDs() []string {
	ml := mapGet(f.root(), "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return nil
	}
	var ids []string
	for _, row := range ml.Content {
		if id := rowName(row); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// SetRow adds the row keyed by id, or replaces the existing one in place,
// carrying over any user-managed presence-based params the new row lacks.
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
		oldParams, newParams := mapGet(old, "litellm_params"), mapGet(row, "litellm_params")
		if oldParams != nil && newParams != nil {
			for _, k := range preservedParamKeys {
				if v := mapGet(oldParams, k); v != nil && mapGet(newParams, k) == nil {
					mapSet(newParams, k, v)
				}
			}
		}
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

// RemoveRow deletes the row keyed by id (no-op when absent). Rows that are
// not mappings are never ours and are left in place.
func (f *File) RemoveRow(id string) {
	ml := mapGet(f.root(), "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return
	}
	kept := ml.Content[:0]
	for _, row := range ml.Content {
		if row.Kind == yaml.MappingNode && rowName(row) == id {
			continue
		}
		kept = append(kept, row)
	}
	ml.Content = kept
}

func isLoopback(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	u, err := url.Parse(n.Value)
	return err == nil && loopbackHosts[strings.ToLower(u.Hostname())]
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
	ml := mapGet(root, "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
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
			reasoningNode, _ := toNode("reasoning_effort") // literal string always succeeds
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{reasoningNode}}
			mapSet(params, "additional_drop_params", seq)
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
