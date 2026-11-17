// The wt-model config home: where the LiteLLM proxy pair (url + api key)
// lives, how it is resolved against the legacy [litellm] fallback, and the
// one writer (SetProxy) — plus the presentation both binaries print through
// (the ConfigError precedent for shared presentation in this package).
//
// Layout under baseConfigHome() (the fixture
// docs/contracts/wt-model-config.sample.toml pins the config.toml shape):
//
//	~/.config/wt-model/config.toml   not secret: url + key file pointer
//	~/.config/wt-model/litellm.key   the api key, mode 0600 (default name)
//
// The api key never enters a TOML file on a new write: it lives in the 0600
// file `key_file` names. The legacy [litellm] table of agent-wt's
// config.toml is a read-only fallback, consulted only when the home has no
// [litellm] table and never written again.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// ProxySource says where the resolved proxy pair came from. Sources never
// mix: one pair comes from exactly one of these.
type ProxySource int

const (
	ProxySourceNone   ProxySource = iota // nothing configured
	ProxySourceHome                      // the wt-model home's [litellm] table
	ProxySourceLegacy                    // the legacy [litellm] table (fallback)
)

// ProxyPair is the LiteLLM proxy's url and api key plus where they came
// from. It is what ResolveProxy resolves and what WriteProxyStatus prints.
type ProxyPair struct {
	URL, APIKey string
	Source      ProxySource
}

// proxyHomeHeader is the fixed comment block SetProxy re-prepends to the
// home's config.toml on every real write (a no-op write skips and keeps any
// hand comments). It is the fixture's opening block verbatim, so the fixture
// test pins both together.
const proxyHomeHeader = "# wt-model's configuration. [litellm] is written only by `wt-model litellm set`\n" +
	"# and `wt litellm set`; the api key is never here — it lives in the 0600 file\n" +
	"# that key_file names.\n"

// defaultProxyKeyFile is the key file's name when key_file is absent.
const defaultProxyKeyFile = "litellm.key"

// proxyHomeDoc is the whole document of the home's config.toml: nothing but
// the [litellm] table, which SetProxy re-encodes in full (a real write drops
// comments and any key outside this type). The pointer distinguishes a
// present-but-empty [litellm] table — authoritative, an empty pair — from
// absence, which falls back to the legacy table.
type proxyHomeDoc struct {
	Litellm *proxyHomeLitellm `toml:"litellm"`
}

// proxyHomeLitellm is the home's [litellm] table: the proxy base URL and the
// key file's name (hand-edited — a credential helper's file can be named
// here; SetProxy never changes it, and absent means litellm.key).
type proxyHomeLitellm struct {
	URL     string `toml:"url"`
	KeyFile string `toml:"key_file,omitempty"`
}

// ProxyHomePath returns the wt-model home's config.toml —
// <baseConfigHome>/wt-model/config.toml — or "" when there is no home, in
// which case nothing resolves or writes.
func ProxyHomePath() string {
	base := baseConfigHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "wt-model", "config.toml")
}

// ResolveProxy resolves the LiteLLM proxy pair: the wt-model home's
// [litellm] table when it has one (present even when empty — then
// authoritative and complete: url from `url`, key from `key_file`'s content,
// and a missing or empty key file is "no key", never a fall-through), else
// the legacy [litellm] table of config.toml (read through readConfigFile —
// no Load, no migrations, no writes), else the unconfigured pair. Sources
// never mix: a home table that supplies no key never borrows the legacy one.
// A parse error in a file the resolution needs is the resolution's error.
func ResolveProxy() (ProxyPair, error) {
	home, present, err := readProxyHome()
	if err != nil {
		return ProxyPair{}, err
	}
	if present {
		key, err := readProxyKey(home)
		if err != nil {
			return ProxyPair{}, err
		}
		return ProxyPair{URL: home.URL, APIKey: key, Source: ProxySourceHome}, nil
	}
	cfg, _, err := readConfigFile()
	if err != nil {
		return ProxyPair{}, err
	}
	if cfg.LitellmTable != nil {
		return ProxyPair{URL: cfg.LitellmTable.URL, APIKey: cfg.LitellmTable.APIKey, Source: ProxySourceLegacy}, nil
	}
	return ProxyPair{Source: ProxySourceNone}, nil
}

// readProxyHome reads the home's [litellm] table. present is false when
// there is no home, no config.toml or no [litellm] table in it — each of
// those falls back to the legacy table. A file that exists but does not
// parse is an error, loud.
func readProxyHome() (lit proxyHomeLitellm, present bool, err error) {
	path := ProxyHomePath()
	if path == "" {
		return lit, false, nil
	}
	doc, _, err := decodeProxyHome(path)
	if err != nil {
		return lit, false, err
	}
	if doc.Litellm == nil {
		return lit, false, nil
	}
	return *doc.Litellm, true, nil
}

// decodeProxyHome reads and decodes the whole home document at path. exists
// is false when the file is not there (a fresh home). A file that exists but
// does not parse is an error, loud: never merge onto a document wt cannot
// read.
func decodeProxyHome(path string) (doc proxyHomeDoc, exists bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return doc, false, nil
	}
	if err != nil {
		return doc, false, err
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return doc, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, true, nil
}

// proxyKeyPath resolves the table's key_file: a leading ~ is expanded
// (ExpandHome) and a relative name is joined beside the home's config.toml.
// An absent name is the default litellm.key.
func proxyKeyPath(lit proxyHomeLitellm) (string, error) {
	name := lit.KeyFile
	if name == "" {
		name = defaultProxyKeyFile
	}
	expanded, err := ExpandHome(name)
	if err != nil {
		return "", fmt.Errorf("key_file %q: %w", name, err)
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(filepath.Dir(ProxyHomePath()), expanded)
	}
	return expanded, nil
}

// readProxyKey reads the key file under the internal/tokenfile rules: a file
// group or other can access is a hard error that keeps its chmod 600
// sentence (a secret others can read must not be used silently), a missing
// or empty file is "no key" — the consumer's existing not-configured path
// reports it — and the key is whitespace-trimmed.
func readProxyKey(lit proxyHomeLitellm) (string, error) {
	path, err := proxyKeyPath(lit)
	if err != nil {
		return "", err
	}
	key, err := tokenfile.Load(path)
	if err == nil {
		return key, nil
	}
	var te *tokenfile.Error
	if errors.As(err, &te) && (te.Kind == tokenfile.KindStat || te.Kind == tokenfile.KindEmpty) {
		return "", nil
	}
	return "", err
}

// SetProxy is the one writer of the wt-model home: it sets the proxy pair's
// url and api key, a nil field leaving that one unchanged. Both files are
// written under one lock, so a reader never sees a url and a key from
// different writers; the TOML keeps its existing mode (0644 when new) and the
// key file is 0600 (umask-independent). A set that changes nothing writes
// nothing — hand comments and the key file's bytes survive — while a real
// TOML write drops comments and re-prepends the fixed header. A symlinked
// config.toml or key file is written through (its link survives) and a
// dangling link refuses the set before anything is touched. Only the new
// home is written: the legacy config.toml is never opened for writing, so a
// broken legacy config or registry can never block the one command that
// fixes the pair.
func SetProxy(url, key *string) error {
	if url == nil && key == nil {
		return nil
	}
	home := ProxyHomePath()
	if home == "" {
		return errors.New("cannot set the proxy pair: no config home (HOME and XDG_CONFIG_HOME are unset)")
	}
	return WithFileLock(home+".lock", func() error {
		cfgTarget, err := writeTarget(home)
		if err != nil {
			return err
		}
		doc, _, err := decodeProxyHome(cfgTarget)
		if err != nil {
			return err
		}
		hadTable := doc.Litellm != nil
		oldLit := proxyHomeLitellm{}
		if hadTable {
			oldLit = *doc.Litellm
		}
		newLit := oldLit
		if !hadTable {
			// A created table names its key file explicitly (the fixture's
			// shape); an existing hand-edited key_file is never changed.
			newLit.KeyFile = defaultProxyKeyFile
		}
		if url != nil {
			newLit.URL = *url
		}
		tomlChanged := !hadTable || newLit != oldLit

		var keyTarget string
		keyChanged := false
		if key != nil {
			keyPath, err := proxyKeyPath(newLit)
			if err != nil {
				return err
			}
			keyTarget, err = writeTarget(keyPath)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(keyTarget)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			// The key's identity is its trimmed content (that is how every
			// reader sees it); a file that merely differs in whitespace is
			// unchanged and its bytes are preserved as written.
			keyChanged = strings.TrimSpace(string(raw)) != strings.TrimSpace(*key)
		}

		// Key first, document last: the TOML is the pointer at the key, so a
		// reader that finds a table already finds its key file written.
		if keyChanged {
			if err := WriteFileAtomic(keyTarget, []byte(*key), 0o600); err != nil {
				return err
			}
		}
		if tomlChanged {
			doc.Litellm = &newLit
			var buf bytes.Buffer
			buf.WriteString(proxyHomeHeader)
			if err := toml.NewEncoder(&buf).Encode(&doc); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if st, err := os.Stat(cfgTarget); err == nil {
				mode = st.Mode().Perm()
			}
			return WriteFileAtomic(cfgTarget, buf.Bytes(), mode)
		}
		return nil
	})
}

// applyProxyState resolves the proxy pair into c's runtime state — ruling 1:
// the merge happens here, at load, so no consumer can forget it (the pair
// lands where finalizeCfg and LoadSession already copied the table, and
// LitellmBaseURL/LitellmAPIKey keep answering from it unchanged). A
// successful resolve lands one source's whole pair — url and key both from
// ResolveProxy's read, never a mix of its read and c's earlier parse — and
// carries the legacy table's retired enabled key along when the legacy
// source won (nothing reads it; saves still round-trip it). When the
// resolution errors, the pre-merge behavior is the whole fallback: the
// legacy table's copy, and nothing when there is none. A broken home or key
// file degrades instead of failing every Load (Load is not credential
// validation; `status` and `set` call ResolveProxy and fail loud there).
func applyProxyState(c *Config) {
	pair, err := ResolveProxy()
	if err == nil {
		c.proxySource = pair.Source
		c.litellm = LitellmState{URL: pair.URL, APIKey: pair.APIKey}
		if pair.Source == ProxySourceLegacy && c.LitellmTable != nil {
			c.litellm.Enabled = c.LitellmTable.Enabled
		}
		return
	}
	if c.LitellmTable != nil {
		c.litellm = *c.LitellmTable
		c.proxySource = ProxySourceLegacy
	}
}

// writeTarget resolves path for a write through any symlink — the
// internal/litellm Save precedent: a dotfiles link survives, its target is
// written. A link whose target is missing is an error (never silently
// replaced by a fresh file, which would quietly un-adopt the user's
// pointer); a path that does not exist at all is written as itself.
func writeTarget(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	_, lerr := os.Lstat(path)
	switch {
	case lerr == nil:
		return "", fmt.Errorf("%s is a dangling symlink (its target is missing)", path)
	case os.IsNotExist(lerr):
		return path, nil
	default:
		return "", lerr
	}
}

// MaskKey is how both binaries show a secret (the LiteLLM proxy key, a
// session's model-server token): "(unset)" when empty, "***" for a key of
// four characters or fewer (its last four would be the whole secret),
// "***" plus the last four otherwise — enough for the user to recognise the
// key without printing it. The key itself is never shown.
func MaskKey(key string) string {
	switch {
	case key == "":
		return "(unset)"
	case len(key) <= 4:
		return "***"
	}
	return "***" + key[len(key)-4:]
}

// WriteProxyStatus prints the `status` document for pair into w. The JSON
// form is exactly the {url, api_key_set} of
// docs/contracts/litellm-cli.sample.json's status member (the fixture's
// golden); the text form is the block `wt litellm status` has always
// printed. The url keeps LitellmBaseURL's trailing-slash trim and prints
// (unset) when empty; the key prints masked (MaskKey) and never itself.
func WriteProxyStatus(w io.Writer, pair ProxyPair, asJSON bool) error {
	url := strings.TrimRight(pair.URL, "/")
	if asJSON {
		return json.NewEncoder(w).Encode(struct {
			URL       string `json:"url"`
			APIKeySet bool   `json:"api_key_set"`
		}{url, pair.APIKey != ""})
	}
	if url == "" {
		url = "(unset)"
	}
	_, err := fmt.Fprintf(w, "litellm:\n  url: %s\n  api_key: %s\n", url, MaskKey(pair.APIKey))
	return err
}

// ProxyFallbackNote is the one note line (no trailing newline) printed where
// the pair is the subject of the command and the legacy [litellm] table
// supplied it (a `status` run, a `wt-model serve` startup): the fallback is
// read-only now, and the note names the writer. Both binary names are
// BinaryName so each binary's note names itself (N1).
func ProxyFallbackNote() string {
	return fmt.Sprintf("%s: note: config.toml: [litellm] is a read-only fallback — '%s litellm set' writes %s now",
		BinaryName, BinaryName, ProxyHomePath())
}
