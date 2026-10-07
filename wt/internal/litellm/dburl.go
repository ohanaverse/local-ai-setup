package litellm

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

// ErrNoDatabase: nothing names the LiteLLM proxy's Postgres database. It is
// the "not configured" answer, distinct from a config.yaml that cannot be
// read (ErrInvalid) — `wt stats` reports both, but only this one means there
// is nothing to fix unless the user wants spend.
var ErrNoDatabase = errors.New("no LiteLLM database configured")

// databaseURLEnv are the variables that name the connection string outright,
// in precedence order. The MODELMAN_ name is the permanent legacy alias.
var databaseURLEnv = []string{"WT_LITELLM_DATABASE_URL", "MODELMAN_LITELLM_DATABASE_URL"}

// environRef is the prefix LiteLLM gives a config value that names an
// environment variable instead of holding the value.
const environRef = "os.environ/"

// proxyDatabaseEnv is the variable LiteLLM itself reads when config.yaml
// names no database_url.
const proxyDatabaseEnv = "DATABASE_URL"

// aliasValue resolves a value written as a whole-value YAML alias —
// `general_settings: *base`, `database_url: *conn` — to the node the anchor
// holds, as LiteLLM's own YAML loader resolves one before reading it. An
// unresolved alias is not "the value is not a string": it is the value the
// anchor holds. The same rule ollamaServeWarnings states and applies for the
// rows it scans (configfile.go). The chase is bounded but not recursive: an
// anchor may name an anchor, and a self-referential one must not loop.
func aliasValue(n *yaml.Node) *yaml.Node {
	for i := 0; i < 32; i++ {
		if n == nil || n.Kind != yaml.AliasNode || n.Alias == nil {
			return n
		}
		n = n.Alias
	}
	return n
}

// DatabaseURL resolves the connection string of the database the LiteLLM
// proxy logs spend to, the way the proxy itself would see it:
// WT_LITELLM_DATABASE_URL, then the legacy MODELMAN_LITELLM_DATABASE_URL,
// then general_settings.database_url in config.yaml (DefaultPath, so
// WT_LITELLM_CONFIG is honored). A config value of the form os.environ/NAME
// is looked up in wt's own environment and then in the proxy's (proxyVar).
// When config.yaml exists and names no database_url, DATABASE_URL is looked
// up the same two ways — LiteLLM's own fallback. A value written as a whole
// YAML alias is resolved first, the way the loader LiteLLM runs resolves
// one (aliasValue).
//
// A value that is empty or only whitespace counts as unset, whatever it came
// from: handed to psql it is a connection string with no host, and libpq
// then tries the local default socket, where some other server may listen.
//
// It only reads. It returns ErrNoDatabase (wrapped, with the reason) when
// nothing names a database, and ErrInvalid when config.yaml exists but
// cannot be parsed. A config.yaml that exists and cannot be read at all
// (permission denied, a directory in its place) is neither: the os error
// comes back as it is, naming the path and the cause. No error it returns
// contains a connection string: the reasons name variables and files, never
// a value.
//
// Neither config.yaml nor the proxy's environment is consulted when the
// registry is redirected and nothing names config.yaml — the rule
// checkRegistryPairing applies to route writes. A scratch-registry run (a
// test, an experiment under XDG_CONFIG_HOME) must not go on to query the
// real proxy's database; the two variables above still work there, because
// naming the database is as explicit as naming config.yaml.
func DatabaseURL() (string, error) {
	for _, k := range databaseURLEnv {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	if _, named := namedPath(); !named && config.RegistryRedirected() {
		return "", fmt.Errorf("%w: the registry is redirected to %s and nothing names config.yaml (set WT_LITELLM_DATABASE_URL, or WT_LITELLM_CONFIG)",
			ErrNoDatabase, config.RegistryPath())
	}
	path := DefaultPath()
	f, err := Open(path)
	if errors.Is(err, ErrMissing) {
		return "", fmt.Errorf("%w: %s does not exist (set WT_LITELLM_DATABASE_URL)", ErrNoDatabase, path)
	}
	if err != nil {
		return "", err
	}
	gs := aliasValue(mergedGet(f.root(), "general_settings"))
	n := aliasValue(mergedGet(gs, "database_url"))
	if n == nil || n.Kind != yaml.ScalarNode || isNull(n) || strings.TrimSpace(n.Value) == "" {
		v, looked := proxyVar(proxyDatabaseEnv)
		if v != "" {
			return v, nil
		}
		// Say what is wrong with the key when it is there: "has no" would
		// send the reader looking for a key they can see in the file.
		has := "has no general_settings.database_url"
		switch {
		case n == nil || isNull(n):
		case n.Kind != yaml.ScalarNode:
			has = "has a general_settings.database_url that is not a string"
		default:
			has = "has a blank general_settings.database_url"
		}
		return "", fmt.Errorf("%w: %s %s, and %s is not set in %s (set WT_LITELLM_DATABASE_URL)",
			ErrNoDatabase, path, has, proxyDatabaseEnv, looked)
	}
	v := strings.TrimSpace(n.Value)
	if !strings.HasPrefix(v, environRef) {
		return v, nil
	}
	// The variable LiteLLM looks up is the value with every "os.environ/"
	// dropped, not only the leading one (get_secret:
	// secret_name.replace("os.environ/", "")) — the same rule
	// ollamaServeWarnings applies to an api_base (configfile.go).
	name := strings.ReplaceAll(v, environRef, "")
	if strings.TrimSpace(name) == "" {
		// No variable is named "": the sentence below would print two holes.
		// The spelling gets its own message, as it does for an api_base
		// (ollamaServeWarnings in configfile.go).
		return "", fmt.Errorf("%w: general_settings.database_url in %s is %s with no variable name (spell it os.environ/<VAR>, or set WT_LITELLM_DATABASE_URL)",
			ErrNoDatabase, path, environRef)
	}
	resolved, looked := proxyVar(name)
	if resolved != "" {
		return resolved, nil
	}
	return "", fmt.Errorf("%w: general_settings.database_url in %s is %s%s, and %s is not set in %s (set it, or WT_LITELLM_DATABASE_URL)",
		ErrNoDatabase, path, environRef, name, name, looked)
}

// proxyVar returns the value of a variable the proxy's configuration relies
// on: from wt's own environment first — what the user set in this shell wins
// — and then from the proxy's (LoadProxyEnv: the LaunchAgent plist's
// EnvironmentVariables). The plist is read only when wt's environment does
// not answer. A blank value is unset in both places.
//
// When there is no value, looked says where it was searched for, for the
// note to quote. When the plist cannot answer, LoadProxyEnv already stands
// wt's environment in for it, so only that is named: the note must not
// claim a file that was not read.
func proxyVar(name string) (value, looked string) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v, ""
	}
	env := loadProxyEnv()
	if env.ambient {
		return "", "wt's environment"
	}
	if v, _ := env.Lookup(name); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), ""
	}
	return "", "wt's environment or " + env.Source
}
