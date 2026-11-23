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
	"# that key_file names. [gateway] is hand-edited (no command writes it) and\n" +
	"# wt-model serve reads it once, at start.\n"

// defaultProxyKeyFile is the key file's name when key_file is absent.
const defaultProxyKeyFile = "litellm.key"

// proxyHomeDoc is the whole document of the home's config.toml: the
// [litellm] table and the hand-edited [gateway] table, which SetProxy
// re-encodes in full (a real write drops comments and any key outside this
// type). The pointer distinguishes a present-but-empty [litellm] table —
// authoritative, an empty pair — from absence, which falls back to the
// legacy table. Gateway is a plain map so SetProxy round-trips whatever the
// operator wrote, and a wrong-typed value there never breaks the [litellm]
// decode: GatewaySettings interprets it (decision 1 of the phase 3 plan).
type proxyHomeDoc struct {
	Litellm *proxyHomeLitellm `toml:"litellm"`
	Gateway map[string]any    `toml:"gateway,omitempty"`
}

// proxyHomeLitellm is the home's [litellm] table: the proxy base URL and the
// key file's name (hand-edited — a credential helper's file can be named
// here; SetProxy never changes it, and absent means litellm.key).
type proxyHomeLitellm struct {
	URL     string `toml:"url"`
	KeyFile string `toml:"key_file,omitempty"`
}

// ModelHome returns the wt-model home directory — <baseConfigHome>/wt-model:
// the proxy pair's config.toml and key, and serve's runtime record — or ""
// when there is no config home.
func ModelHome() string {
	base := baseConfigHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "wt-model")
}

// ProxyHomePath returns the wt-model home's config.toml —
// <ModelHome>/config.toml — or "" when there is no home, in which case
// nothing resolves or writes.
func ProxyHomePath() string {
	home := ModelHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "config.toml")
}

// ResolveProxy resolves the LiteLLM proxy pair: the wt-model home's
// [litellm] table when it has one (present even when empty — then
// authoritative and complete: url from `url`, key from `key_file`'s content,
// and a missing or empty key file is "no key", never a fall-through), else
// the legacy [litellm] table of config.toml (read through readConfigFile —
// no Load, no migrations, no writes), else the unconfigured pair. Sources
// never mix: a home table that supplies no key never borrows the legacy one.
// A parse error in a file the resolution needs is the resolution's error and
// the zero pair — never another source's values (fix round 1, Findings 1/3).
//
// The home's two reads (its TOML and its key file) run under the flock
// SetProxy writes with, so one resolve sees one writer's whole pair — never a
// url and a key from different writes. An absent home config.toml takes no
// lock: it is the writer's pre-publication state, coherent by construction
// (SetProxy publishes the TOML last), and the legacy read below is one file's
// one parse.
func ResolveProxy() (ProxyPair, error) {
	return resolveProxy(func() (*LitellmState, error) {
		cfg, _, err := readConfigFile()
		if err != nil {
			return nil, err
		}
		return cfg.LitellmTable, nil
	})
}

// resolveProxy is ResolveProxy with the legacy table supplied by the caller:
// ResolveProxy reads it fresh (readConfigFile), while a load hands in the table
// it has already parsed (applyProxyState), so a Load reads config.toml once and
// the legacy pair and its retired enabled key come from that one parse. legacy
// runs only when the home holds no [litellm] table.
func resolveProxy(legacy func() (*LitellmState, error)) (ProxyPair, error) {
	home := ProxyHomePath()
	if home == "" {
		// No config home at all: nothing resolves (ProxyHomePath's contract)
		// — the legacy read below would open the relative agent-wt/config.toml
		// under the process's working directory (fix round 2, Finding 3).
		return ProxyPair{Source: ProxySourceNone}, nil
	}
	switch _, serr := os.Stat(home); {
	case serr == nil:
		pair, found, err := resolveProxyHome(home)
		if err != nil {
			return ProxyPair{}, err
		}
		if found {
			return pair, nil
		}
	case !os.IsNotExist(serr):
		return ProxyPair{}, serr
	default:
		// A config.toml link whose target is missing (a dotfiles link to an
		// unmounted volume) is not "no home": falling through would silently
		// serve the legacy pair. Loud, like SetProxy's writeTarget.
		// Re-stat after the Lstat: a first SetProxy may publish the file
		// between the two calls, and that is the pre-publication state, not
		// a dangling link.
		if lst, lerr := os.Lstat(home); lerr == nil && lst.Mode()&os.ModeSymlink != 0 {
			if _, serr := os.Stat(home); os.IsNotExist(serr) {
				return ProxyPair{}, fmt.Errorf("%s is a dangling symlink (its target is missing)", home)
			}
			return resolveProxy(legacy)
		}
	}
	table, err := legacy()
	if err != nil {
		return ProxyPair{}, err
	}
	if table != nil {
		return ProxyPair{URL: table.URL, APIKey: table.APIKey, Source: ProxySourceLegacy}, nil
	}
	return ProxyPair{Source: ProxySourceNone}, nil
}

// resolveProxyHome resolves the home's pair under a shared flock — readers
// run beside one another; SetProxy's write keeps the exclusive one (see
// ResolveProxy). found is false when the home's config.toml holds no
// [litellm] table — the file without the table is the legacy fallback's case,
// not an empty pair.
func resolveProxyHome(home string) (pair ProxyPair, found bool, err error) {
	err = WithFileLockShared(home+".lock", func() error {
		lit, present, rerr := readProxyHome(home)
		if rerr != nil {
			return rerr
		}
		if !present {
			return nil
		}
		key, rerr := readProxyKey(lit)
		if rerr != nil {
			return rerr
		}
		pair = ProxyPair{URL: lit.URL, APIKey: key, Source: ProxySourceHome}
		found = true
		return nil
	})
	return pair, found, err
}

// readProxyHome reads the [litellm] table of the home config.toml at path.
// present is false when there is no config.toml or no [litellm] table in it —
// each of those falls back to the legacy table. A file that exists but does
// not parse is an error, loud.
func readProxyHome(path string) (lit proxyHomeLitellm, present bool, err error) {
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
// read. The error quotes no value from the file at all (valueFreeParseError,
// secretSafe's rule over every key: #640, and the [gateway] table serve
// refuses to start on).
func decodeProxyHome(path string) (doc proxyHomeDoc, exists bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return doc, false, nil
	}
	if err != nil {
		return doc, false, err
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return doc, false, fmt.Errorf("parse %s: %w", path, valueFreeParseError(err, data))
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

// readProxyKey reads the key file the table names under the one key-file
// reading, keyToken.
func readProxyKey(lit proxyHomeLitellm) (string, error) {
	path, err := proxyKeyPath(lit)
	if err != nil {
		return "", err
	}
	return keyToken(path)
}

// keyToken is the one reading of a key file's key (fix round 1, Finding 2):
// tokenfile.Load — a file group or other can access is a hard error keeping
// its chmod 600 sentence (a secret others can read must not be used
// silently) and the key is whitespace-trimmed — with a missing or empty file
// mapped to "no key" (""), the consumer's existing not-configured path
// reporting it. Only those two mean "no key": any other stat failure (EACCES,
// ENOTDIR, …) is the error it is (fix round 2, Finding 1) — a key file wt
// cannot even inspect must never read as unset. Both readers go through here:
// ResolveProxy's resolve and SetProxy's no-op comparison, so "what the key
// is" is defined once and a loose or unreadable file errors in both
// directions.
func keyToken(path string) (string, error) {
	key, _, err := keyTokenState(path)
	return key, err
}

// keyTokenState is keyToken plus presence: a missing file (present false, key
// "") is distinct from an existing empty one (present true, key ""), because
// the spec requires a fresh `set --api-key ""` to write the empty 0600 file —
// absence is not "unchanged" (fix round 2, Finding 2).
func keyTokenState(path string) (key string, present bool, err error) {
	key, err = tokenfile.Load(path)
	if err == nil {
		return key, true, nil
	}
	var te *tokenfile.Error
	if errors.As(err, &te) {
		switch {
		case te.Kind == tokenfile.KindEmpty:
			return "", true, nil
		case te.Kind == tokenfile.KindStat && errors.Is(err, os.ErrNotExist):
			return "", false, nil
		}
	}
	return "", false, err
}

// SetProxy is the one writer of the wt-model home: it sets the proxy pair's
// url and api key, a nil field leaving that one unchanged. Both files are
// written under one lock and ResolveProxy reads them under the same lock
// (resolveProxyHome), so no reader ever sees a url and a key from different
// writes; the TOML keeps its existing mode (0644 when new) and the key file
// is 0600 (umask-independent). A set that changes nothing writes nothing —
// hand comments and the key file's bytes survive — while a real TOML write
// drops comments and re-prepends the fixed header. A symlinked config.toml
// or key file is written through (its link survives) and a dangling link
// refuses the set before anything is touched. Only the new home is written:
// the legacy config.toml is never opened for writing, so a broken legacy
// config or registry can never block the one command that fixes the pair.
// A url holding an '@' is refused first, writing nothing (ErrProxyURLHasAt).
// A set that would create the home's [litellm] table and leaves out a field
// the legacy table holds is refused, writing nothing (refusePartialFirstSet).
func SetProxy(url, key *string) error {
	if url == nil && key == nil {
		return nil
	}
	// A url with an '@' is refused before anything is read or written
	// (#640); a key-only set over a stored one still works.
	if url != nil && ProxyURLHasAt(*url) {
		return ErrProxyURLHasAt
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
		// The first set creates the authoritative table, and sources never
		// mix (ruling 4): a field it leaves out would read empty from then
		// on, silently dropping what the legacy table supplied. So a first
		// set that leaves out a field the legacy table holds is refused —
		// nothing is copied from it.
		if !hadTable {
			if err := refusePartialFirstSet(cfgTarget, url, key); err != nil {
				return err
			}
		}
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
			// The comparison is the key's identity as every reader sees it
			// — keyTokenState (tokenfile.Load) on both sides, one definition
			// of "what the key is": a file that merely differs in whitespace
			// is unchanged and its bytes are preserved as written, and a loose
			// or unreadable file errors out of the set too (fix round 1,
			// Finding 2) — never a silent success over a key every read
			// refuses. An absent file always writes (fix round 2, Finding 2):
			// a fresh explicit clear must create the empty 0600 file the spec
			// requires, not skip because "" against absence reads unchanged.
			current, present, err := keyTokenState(keyTarget)
			if err != nil {
				return err
			}
			keyChanged = !present || current != strings.TrimSpace(*key)
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

// refusePartialFirstSet refuses a first set (no home [litellm] table yet)
// that leaves out a field the legacy [litellm] table holds: the new table is
// authoritative, so that value would silently stop resolving. A legacy
// config.toml that cannot be read is not a reason to refuse — the set is the
// command that replaces it (SetProxy's contract), and there is no value wt
// can see to drop.
func refusePartialFirstSet(cfgTarget string, url, key *string) error {
	if url != nil && key != nil {
		return nil
	}
	cfg, _, err := readConfigFile()
	if err != nil || cfg.LitellmTable == nil {
		return nil
	}
	if (url == nil && cfg.LitellmTable.URL != "") || (key == nil && cfg.LitellmTable.APIKey != "") {
		return fmt.Errorf("%s has no [litellm] table yet and the legacy [litellm] table of config.toml holds the pair, so the first set needs both --url and --api-key (nothing is copied from the legacy table)", cfgTarget)
	}
	return nil
}

// applyProxyState resolves the proxy pair into c's runtime state — ruling 1:
// the merge happens here, at load, so no consumer can forget it (the pair
// lands where finalizeCfg already copied the table, and
// LitellmBaseURL/LitellmAPIKey keep answering from it unchanged). A
// successful resolve lands one source's whole pair — the legacy table is c's
// own parse (no second read of config.toml), so url, key and the retired
// enabled key carried along when the legacy source won (nothing reads it;
// saves still round-trip it) all come from one read. A resolution error
// leaves the pair empty and is recorded as c.ProxyErr (a *ProxyHomeError):
// nothing falls back — an unparseable home config.toml or a loose/unreadable
// key file must never silently serve the legacy values (fix round 1,
// Findings 1/3) — but the load itself goes on, and only the commands that use
// the pair fail on it (`wt-model serve`, a proxy restart or probe; `status`
// fails loud through ResolveProxy, `set` through SetProxy's own reads).
func applyProxyState(c *Config) {
	pair, err := resolveProxy(func() (*LitellmState, error) { return c.LitellmTable, nil })
	if err != nil {
		c.proxyErr = &ProxyHomeError{Err: err}
		c.proxySource = ProxySourceNone
		c.litellm = LitellmState{}
		return
	}
	c.proxyErr = nil
	c.proxySource = pair.Source
	c.litellm = LitellmState{URL: pair.URL, APIKey: pair.APIKey}
	if pair.Source == ProxySourceLegacy {
		c.litellm.Enabled = c.LitellmTable.Enabled
	}
}

// ProxyHomeError is a load's failure to resolve the proxy pair from the
// wt-model config home (an unparseable home config.toml, a dangling link, a
// loose or unreadable key file), what (*Config).ProxyErr returns. It is its
// own type so LoadFixHint does not send the user to `wt config`, which cannot
// touch the home.
type ProxyHomeError struct{ Err error }

func (e *ProxyHomeError) Error() string { return e.Err.Error() }

// Unwrap exposes Err to errors.Is/errors.As.
func (e *ProxyHomeError) Unwrap() error { return e.Err }

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
// (unset) when empty, and ProxyURLHidden when it holds an '@'; the key
// prints masked (MaskKey) and never itself.
func WriteProxyStatus(w io.Writer, pair ProxyPair, asJSON bool) error {
	url := ShownProxyURL(strings.TrimRight(pair.URL, "/"))
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

// ShowProxyStatus is both binaries' `litellm status`: the pair as every
// reader resolves it (ResolveProxy), printed through WriteProxyStatus, and the
// fallback note on errOut when the legacy table supplied it (ruling 7) — one
// body, so the twins cannot diverge.
func ShowProxyStatus(out, errOut io.Writer, asJSON bool) error {
	pair, err := ResolveProxy()
	if err != nil {
		return err
	}
	if err := WriteProxyStatus(out, pair, asJSON); err != nil {
		return err
	}
	if pair.Source == ProxySourceLegacy {
		fmt.Fprintln(errOut, ProxyFallbackNote())
	}
	return nil
}

// GatewayConf is the home's [gateway] table as serve reads it at start.
type GatewayConf struct {
	// SubscriptionPassthrough turns on the gateway's header mode
	// (x-wt-model-token beside the client's own claude.ai login). Off unless
	// the table says subscription_passthrough = true.
	SubscriptionPassthrough bool
}

// gatewayPassthroughKey is the host switch's key under [gateway].
const gatewayPassthroughKey = "subscription_passthrough"

// GatewaySettings reads the wt-model home's [gateway] table (ProxyHomePath,
// through decodeProxyHome, under the same shared lock as the pair). No file,
// no table or an absent key is the zero value. A key holding the wrong type
// ("true", 1) is off plus one warning naming the key and the type it needs:
// off is the safe direction. A file that does not parse is an error, worded
// without any value from it. No code writes this table.
func GatewaySettings() (GatewayConf, []string, error) {
	home := ProxyHomePath()
	if home == "" {
		return GatewayConf{}, nil, nil
	}
	var doc proxyHomeDoc
	err := WithFileLockShared(home+".lock", func() error {
		var derr error
		doc, _, derr = decodeProxyHome(home)
		return derr
	})
	if err != nil {
		return GatewayConf{}, nil, err
	}
	gc, warns := gatewayFromDoc(doc)
	return gc, warns, nil
}

// gatewayFromDoc interprets a decoded home document's [gateway] table.
func gatewayFromDoc(doc proxyHomeDoc) (GatewayConf, []string) {
	var gc GatewayConf
	var warns []string
	if v, ok := doc.Gateway[gatewayPassthroughKey]; ok {
		switch b, isBool := v.(bool); {
		case isBool:
			gc.SubscriptionPassthrough = b
		default:
			warns = append(warns, fmt.Sprintf("[gateway] %s in %s must be a boolean (true or false), not %s; subscription passthrough is off",
				gatewayPassthroughKey, ProxyHomePath(), tomlTypeName(v)))
		}
	}
	return gc, warns
}

// tomlTypeName names a decoded TOML value's type for a message, never the
// value itself.
func tomlTypeName(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case []any, []map[string]any:
		return "an array"
	case map[string]any:
		return "a table"
	default:
		return "a date or time"
	}
}
