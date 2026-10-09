package litellm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubProxyEnv replaces the proxy-environment seam (loadProxyEnv) for this
// test with a LaunchAgent environment holding vars, restored on cleanup. No
// test here may read the developer's real plist: it can hold the real
// connection string.
func stubProxyEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	old := loadProxyEnv
	loadProxyEnv = func() ProxyEnv {
		return ProxyEnv{Source: "the test proxy environment", vars: vars}
	}
	t.Cleanup(func() { loadProxyEnv = old })
}

// dbConfig points WT_LITELLM_CONFIG at a fresh config.yaml holding body (no
// file when body is ""), clears every variable DatabaseURL reads and gives
// the proxy an empty environment, so no test here can fall through to the
// developer's real config.yaml or plist, or pick up a connection string from
// their shell. It returns the file's path.
func dbConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WT_LITELLM_CONFIG", p)
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	t.Setenv("WT_LITELLM_DATABASE_URL", "")
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")
	stubProxyEnv(t, nil)
	return p
}

// TestDatabaseURLPrecedence pins the lookup order the spec fixes:
// WT_LITELLM_DATABASE_URL, then the legacy MODELMAN_LITELLM_DATABASE_URL,
// then general_settings.database_url in config.yaml. The legacy name is a
// permanent alias, so a shell profile that still exports it must keep
// working with `wt stats`; and the WT_ name must win so it can override both.
func TestDatabaseURLPrecedence(t *testing.T) {
	const fromFile = "general_settings:\n  database_url: postgresql://file/db\n"
	cases := []struct {
		name      string
		wt, alias string
		want      string
	}{
		{"file only", "", "", "postgresql://file/db"},
		{"legacy env beats the file", "", "postgresql://legacy/db", "postgresql://legacy/db"},
		{"WT env beats both", "postgresql://wt/db", "postgresql://legacy/db", "postgresql://wt/db"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbConfig(t, fromFile)
			t.Setenv("WT_LITELLM_DATABASE_URL", c.wt)
			t.Setenv("MODELMAN_LITELLM_DATABASE_URL", c.alias)
			got, err := DatabaseURL()
			if err != nil || got != c.want {
				t.Fatalf("DatabaseURL() = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// TestDatabaseURLEnvNeedsNoConfigFile verifies a connection string from the
// environment is returned without config.yaml being opened at all: the file
// here is missing, and in a second run unparseable. A machine whose proxy
// config lives elsewhere depends on it: the variable is its only way to
// name the database.
func TestDatabaseURLEnvNeedsNoConfigFile(t *testing.T) {
	for _, body := range []string{"", "general_settings: [unclosed\n"} {
		dbConfig(t, body)
		t.Setenv("WT_LITELLM_DATABASE_URL", "postgresql://wt/db")
		if got, err := DatabaseURL(); err != nil || got != "postgresql://wt/db" {
			t.Errorf("config body %q: DatabaseURL() = %q, %v; want the env value", body, got, err)
		}
	}
}

// TestDatabaseURLResolvesAliasedValues verifies a value written as a YAML
// alias — the database_url itself, or the whole general_settings mapping —
// is resolved to what its anchor holds, the way LiteLLM's loader resolves
// one. Without the resolution the alias node reads as "not a string" and
// wt stats reports no LiteLLM database on a machine whose proxy is logging
// spend through exactly such a config.yaml.
func TestDatabaseURLResolvesAliasedValues(t *testing.T) {
	for _, body := range []string{
		// The database_url itself is an alias.
		"conn: &conn postgresql://aliased/db\ngeneral_settings:\n  database_url: *conn\n",
		// The whole general_settings mapping is an alias.
		"base: &gs\n  database_url: postgresql://aliased/db\ngeneral_settings: *gs\n",
	} {
		dbConfig(t, body)
		got, err := DatabaseURL()
		if err != nil || got != "postgresql://aliased/db" {
			t.Errorf("body:\n%s\nDatabaseURL() = %q, %v; want the anchor's value", body, got, err)
		}
	}
}

// TestDatabaseURLResolvesAnEnvironReference verifies a config value written
// the way LiteLLM's own docs write it — os.environ/NAME — is resolved the way
// the proxy would see it: from wt's own environment, else from the proxy's
// LaunchAgent environment, with wt's own winning when both have it. On the
// usual install the variable is set only in the plist, so a lookup in wt's
// shell alone reports "not configured" on a machine whose proxy is logging
// spend. Unset in both, it is "not configured" with a message that names the
// variable and both places and holds no value from either.
func TestDatabaseURLResolvesAnEnvironReference(t *testing.T) {
	p := dbConfig(t, "general_settings:\n  database_url: os.environ/WT_TEST_DB_URL\n")

	// Only the proxy has it.
	stubProxyEnv(t, map[string]string{"WT_TEST_DB_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); err != nil || got != "postgresql://proxy/db" {
		t.Fatalf("set only for the proxy: DatabaseURL() = %q, %v; want the proxy's value", got, err)
	}

	// Both have it: the shell wt was typed in wins.
	t.Setenv("WT_TEST_DB_URL", "postgresql://shell/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
		t.Fatalf("set in both: DatabaseURL() = %q, %v; want wt's own value", got, err)
	}

	// Only wt's environment has it.
	stubProxyEnv(t, nil)
	if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
		t.Fatalf("set only for wt: DatabaseURL() = %q, %v; want wt's own value", got, err)
	}

	// Neither has it. The proxy does have other variables, one of them a
	// connection string: none of their values may reach the message.
	t.Setenv("WT_TEST_DB_URL", "")
	stubProxyEnv(t, map[string]string{"OTHER_DB_URL": "postgresql://u:FAKEsecret@other/db"})
	_, err := DatabaseURL()
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("set in neither: err = %v, want ErrNoDatabase", err)
	}
	want := "no LiteLLM database configured: general_settings.database_url in " + p +
		" is os.environ/WT_TEST_DB_URL, and WT_TEST_DB_URL is not set in wt's environment or the test proxy environment" +
		" (set it, or WT_LITELLM_DATABASE_URL)"
	if err.Error() != want {
		t.Errorf("err = %q\nwant  %q", err, want)
	}
	if strings.Contains(err.Error(), "FAKEsecret") || strings.Contains(err.Error(), "postgresql://") {
		t.Errorf("err = %q quotes a value from the proxy's environment", err)
	}

	// The prefix with no name after it: there is no variable to report as
	// unset, so the message says that instead of printing two holes.
	p = dbConfig(t, "general_settings:\n  database_url: os.environ/\n")
	_, err = DatabaseURL()
	if !errors.Is(err, ErrNoDatabase) || !strings.Contains(err.Error(), "is os.environ/ with no variable name") || !strings.Contains(err.Error(), p) {
		t.Errorf("err = %v, want ErrNoDatabase saying the reference names no variable, and naming %s", err, p)
	}
	if strings.Contains(err.Error(), "  ") {
		t.Errorf("err = %q has a hole where a variable name should be", err)
	}
}

// TestDatabaseURLFallsBackToDatabaseURL verifies LiteLLM's own fallback: a
// config.yaml that names no database_url leaves the proxy reading
// DATABASE_URL from its environment, which is how a LaunchAgent install is
// usually wired. wt looks the variable up the same two ways as a reference —
// its own environment, then the proxy's — and says so by name when neither
// has it. A missing config.yaml is not that case: with no proxy config there
// is nothing to say a DATABASE_URL in the shell belongs to LiteLLM.
func TestDatabaseURLFallsBackToDatabaseURL(t *testing.T) {
	const noURL = "general_settings:\n  master_key: x\n"

	p := dbConfig(t, noURL)
	stubProxyEnv(t, map[string]string{"DATABASE_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); err != nil || got != "postgresql://proxy/db" {
		t.Fatalf("DATABASE_URL set only for the proxy: DatabaseURL() = %q, %v; want the proxy's value", got, err)
	}
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
		t.Fatalf("DATABASE_URL set in both: DatabaseURL() = %q, %v; want wt's own value", got, err)
	}

	t.Setenv("DATABASE_URL", "")
	stubProxyEnv(t, nil)
	_, err := DatabaseURL()
	want := "no LiteLLM database configured: " + p + " has no general_settings.database_url," +
		" and DATABASE_URL is not set in wt's environment or the test proxy environment (set WT_LITELLM_DATABASE_URL)"
	if !errors.Is(err, ErrNoDatabase) || err.Error() != want {
		t.Errorf("DATABASE_URL set in neither: err = %q\nwant  %q", err, want)
	}

	// A literal database_url is never overridden by DATABASE_URL.
	dbConfig(t, "general_settings:\n  database_url: postgresql://file/db\n")
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://file/db" {
		t.Errorf("config.yaml names a database: DatabaseURL() = %q, %v; want the file's value", got, err)
	}

	// No config.yaml at all: DATABASE_URL is not consulted.
	dbConfig(t, "")
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	stubProxyEnv(t, map[string]string{"DATABASE_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); got != "" || !errors.Is(err, ErrNoDatabase) {
		t.Errorf("no config.yaml: DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase", got, err)
	}
}

// TestDatabaseURLWhenThePlistCannotBeRead verifies the lookup with the real
// plist reader and a LaunchAgent plist that cannot answer — missing, binary,
// or bypassed by a configured restart command. wt's own environment is then
// the only source: a variable set there still resolves, and one that is not
// is reported as unset in wt's environment, with no claim about a plist that
// was never read. The plist here is a temp file named by WT_LITELLM_PLIST.
func TestDatabaseURLWhenThePlistCannotBeRead(t *testing.T) {
	dbConfig(t, "general_settings:\n  database_url: os.environ/WT_TEST_DB_URL\n")
	old := loadProxyEnv
	loadProxyEnv = realLoadProxyEnv
	t.Cleanup(func() { loadProxyEnv = old })

	check := func(label string) {
		t.Helper()
		t.Setenv("WT_TEST_DB_URL", "postgresql://shell/db")
		if got, err := DatabaseURL(); err != nil || got != "postgresql://shell/db" {
			t.Errorf("%s: DatabaseURL() = %q, %v; want wt's own value", label, got, err)
		}
		t.Setenv("WT_TEST_DB_URL", "")
		_, err := DatabaseURL()
		if !errors.Is(err, ErrNoDatabase) || !strings.HasSuffix(err.Error(), "and WT_TEST_DB_URL is not set in wt's environment (set it, or WT_LITELLM_DATABASE_URL)") {
			t.Errorf("%s: err = %v, want ErrNoDatabase naming wt's environment only", label, err)
		}
		if err != nil && strings.Contains(err.Error(), "LaunchAgent") {
			t.Errorf("%s: err = %q claims the LaunchAgent plist was consulted", label, err)
		}
	}
	writePlist(t, "")
	check("no plist")
	writePlist(t, "bplist00\x01\x02binary")
	check("binary plist")
	writePlist(t, proxyPlist)
	t.Setenv("WT_LITELLM_RESTART_CMD", "systemctl restart litellm")
	check("a restart command is configured")

	// The same reader, with a plist it can read: the value comes from the
	// file, and the note for a variable it lacks names the file.
	plist := writePlist(t, proxyPlist)
	dbConfig(t, "general_settings:\n  database_url: os.environ/IN_PLIST\n")
	loadProxyEnv = realLoadProxyEnv
	if got, err := DatabaseURL(); err != nil || got != "http://box:11434" {
		t.Errorf("readable plist: DatabaseURL() = %q, %v; want the plist's value for IN_PLIST", got, err)
	}
	dbConfig(t, "general_settings:\n  database_url: os.environ/NOT_IN_PLIST\n")
	loadProxyEnv = realLoadProxyEnv
	if _, err := DatabaseURL(); err == nil || !strings.Contains(err.Error(), "NOT_IN_PLIST is not set in wt's environment or the proxy LaunchAgent's EnvironmentVariables ("+plist+")") {
		t.Errorf("readable plist without the variable: err = %v, want it to name the plist %s", err, plist)
	}
}

// TestDatabaseURLBlankCountsAsUnset verifies a value that is empty or only
// whitespace is "unset" at every source: each of the two variables, the
// config.yaml value, a referenced variable in wt's environment and in the
// proxy's, and DATABASE_URL. A blank string handed to psql is a connection
// string with no host, and libpq then tries the local default socket — a
// server that is not the proxy's database. So the lookup moves on to the
// next source, and with none left answers ErrNoDatabase, never "".
func TestDatabaseURLBlankCountsAsUnset(t *testing.T) {
	const blank = " \t "
	const fromFile = "general_settings:\n  database_url: postgresql://file/db\n"

	// A blank WT_ variable does not win; the legacy variable is next.
	dbConfig(t, fromFile)
	t.Setenv("WT_LITELLM_DATABASE_URL", blank)
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", "postgresql://legacy/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://legacy/db" {
		t.Errorf("blank WT variable: DatabaseURL() = %q, %v; want the legacy variable's value", got, err)
	}
	// Both blank: config.yaml is next.
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", blank)
	if got, err := DatabaseURL(); err != nil || got != "postgresql://file/db" {
		t.Errorf("both variables blank: DatabaseURL() = %q, %v; want the file's value", got, err)
	}

	// A blank value in config.yaml is no database_url: DATABASE_URL is next,
	// and a blank DATABASE_URL in wt's environment yields to the proxy's.
	dbConfig(t, "general_settings:\n  database_url: \"   \"\n")
	t.Setenv("DATABASE_URL", blank)
	stubProxyEnv(t, map[string]string{"DATABASE_URL": "postgresql://proxy/db"})
	if got, err := DatabaseURL(); err != nil || got != "postgresql://proxy/db" {
		t.Errorf("blank config value and blank DATABASE_URL: DatabaseURL() = %q, %v; want the proxy's value", got, err)
	}

	// Blank everywhere a reference can be resolved: not configured.
	dbConfig(t, "general_settings:\n  database_url: os.environ/WT_TEST_DB_URL\n")
	t.Setenv("WT_TEST_DB_URL", blank)
	stubProxyEnv(t, map[string]string{"WT_TEST_DB_URL": blank})
	got, err := DatabaseURL()
	if got != "" || !errors.Is(err, ErrNoDatabase) ||
		!strings.HasSuffix(err.Error(), "and WT_TEST_DB_URL is not set in wt's environment or the test proxy environment (set it, or WT_LITELLM_DATABASE_URL)") {
		t.Errorf("blank in both environments: DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase naming the variable and both places", got, err)
	}

	// Surrounding whitespace on a real value is dropped, not passed to psql.
	dbConfig(t, "")
	t.Setenv("WT_LITELLM_DATABASE_URL", "  postgresql://wt/db\n")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://wt/db" {
		t.Errorf("padded value: DatabaseURL() = %q, %v; want it trimmed", got, err)
	}
}

// TestProxyEnvLookupReturnsTheValue verifies ProxyEnv.Lookup, the value
// behind IsSet: the plist's string for a variable it sets (the empty string
// included), nothing for one only wt's shell has, and wt's own environment
// when that stands in for an unreadable plist. DatabaseURL resolves the
// proxy's connection string through it.
func TestProxyEnvLookupReturnsTheValue(t *testing.T) {
	writePlist(t, proxyPlist)
	t.Setenv("ONLY_IN_SHELL", "x")
	env := LoadProxyEnv()
	for name, want := range map[string]struct {
		value string
		set   bool
	}{"IN_PLIST": {"http://box:11434", true}, "EMPTY_IN_PLIST": {"", true}, "ONLY_IN_SHELL": {"", false}} {
		if got, ok := env.Lookup(name); got != want.value || ok != want.set {
			t.Errorf("Lookup(%q) = %q, %v; want %q, %v", name, got, ok, want.value, want.set)
		}
	}
	writePlist(t, "")
	if got, ok := LoadProxyEnv().Lookup("ONLY_IN_SHELL"); got != "x" || !ok {
		t.Errorf("no plist: Lookup(ONLY_IN_SHELL) = %q, %v; want wt's own value", got, ok)
	}
}

// TestDatabaseURLNotConfigured verifies each way of having no database is
// ErrNoDatabase and names config.yaml: no file, no general_settings, no
// database_url, a null, empty or blank value, a value that is not a string,
// an os.environ/ reference that names no variable — all with DATABASE_URL
// set nowhere. `wt stats` turns ErrNoDatabase into one quiet note and still
// prints launches; any other error here would read as a broken install on a
// machine that simply runs LiteLLM without spend logging. The note also
// says what is wrong with a key that is present: "has no database_url" for
// a key the reader can see in the file sends them looking in the wrong
// place.
func TestDatabaseURLNotConfigured(t *testing.T) {
	const (
		absent  = " has no general_settings.database_url, and DATABASE_URL is not set"
		blank   = " has a blank general_settings.database_url, and DATABASE_URL is not set"
		notText = " has a general_settings.database_url that is not a string, and DATABASE_URL is not set"
	)
	cases := map[string]struct{ body, says string }{
		"missing file":        {"", " does not exist"},
		"no general_settings": {"model_list: []\n", absent},
		"no database_url":     {"general_settings:\n  master_key: x\n", absent},
		"null value":          {"general_settings:\n  database_url:\n", absent},
		"empty value":         {"general_settings:\n  database_url: \"\"\n", blank},
		"blank value":         {"general_settings:\n  database_url: \"  \"\n", blank},
		"a mapping":           {"general_settings:\n  database_url:\n    host: x\n", notText},
		"a list":              {"general_settings:\n  database_url: [x]\n", notText},
		"bare os.environ/":    {"general_settings:\n  database_url: os.environ/\n", " with no variable name"},
		"settings not a map":  {"general_settings: 3\n", absent},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := dbConfig(t, c.body)
			got, err := DatabaseURL()
			if got != "" || !errors.Is(err, ErrNoDatabase) {
				t.Fatalf("DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase", got, err)
			}
			if !strings.Contains(err.Error(), p) {
				t.Errorf("err = %q, want it to name %s", err, p)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("err = %q, want it to say %q", err, c.says)
			}
		})
	}
}

// TestDatabaseURLInvalidConfigIsNotNoDatabase verifies a config.yaml that
// is not a usable proxy config is ErrInvalid, not ErrNoDatabase: the user
// has a proxy config and it is broken, which `wt stats` must say rather
// than report "nothing configured". That covers a file that does not parse
// and one that holds no mapping at all — a blank line or only a comment,
// which is what a freshly created config.yaml looks like and what Open
// refuses for every other reader too. It also pins that the file is not
// rewritten by the read.
func TestDatabaseURLInvalidConfigIsNotNoDatabase(t *testing.T) {
	for name, body := range map[string]string{
		"does not parse": "general_settings: [unclosed\n",
		"a blank line":   "\n",
		"only a comment": "# nothing here yet\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := dbConfig(t, body)
			before, _ := os.ReadFile(p)
			_, err := DatabaseURL()
			if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrNoDatabase) {
				t.Fatalf("err = %v, want ErrInvalid and not ErrNoDatabase", err)
			}
			if after, _ := os.ReadFile(p); string(after) != string(before) {
				t.Errorf("config.yaml changed:\n%s", after)
			}
		})
	}
}

// TestDatabaseURLUnreadableConfig verifies a config.yaml that exists and
// cannot be read — here a directory in its place, which fails the same way
// for every user, root included — is reported as the os error: neither "no
// database configured" nor "invalid YAML", and naming the path. `wt stats`
// prints it as its "spend unavailable" note, and the cause (is a directory,
// permission denied) is what tells the user which of the two to fix.
func TestDatabaseURLUnreadableConfig(t *testing.T) {
	p := dbConfig(t, "")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := DatabaseURL()
	if got != "" || err == nil || errors.Is(err, ErrNoDatabase) || errors.Is(err, ErrInvalid) {
		t.Fatalf("DatabaseURL() = %q, %v; want \"\" and the os error, not ErrNoDatabase or ErrInvalid", got, err)
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("err = %q, want it to name %s", err, p)
	}
}

// TestDatabaseURLReadsThroughAMergeKey verifies a database_url supplied by a
// YAML merge ("<<: *defaults") is found, as the proxy itself would see it.
// Missed, `wt stats` says no database is configured on a machine whose proxy
// is logging spend.
func TestDatabaseURLReadsThroughAMergeKey(t *testing.T) {
	dbConfig(t, "defaults: &d\n  database_url: postgresql://merged/db\ngeneral_settings:\n  <<: *d\n  master_key: x\n")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://merged/db" {
		t.Fatalf("DatabaseURL() = %q, %v; want the merged value", got, err)
	}
}

// TestDatabaseURLSkipsTheDefaultConfigUnderARedirectedRegistry verifies the
// scratch-run guard: with the registry redirected and nothing naming
// config.yaml, DatabaseURL answers ErrNoDatabase and never returns the
// database_url the default config.yaml holds. Without it, every `wt stats`
// in a test or under a scratch XDG_CONFIG_HOME would query the developer's
// real spend database. The guard comes before the DATABASE_URL fallback and
// before the proxy's environment is read at all, so a scratch run cannot
// pick the real connection string out of the LaunchAgent plist either.
// Naming the database or config.yaml lifts the guard.
func TestDatabaseURLSkipsTheDefaultConfigUnderARedirectedRegistry(t *testing.T) {
	p := redirectedRegistry(t)
	t.Setenv("WT_LITELLM_DATABASE_URL", "")
	t.Setenv("MODELMAN_LITELLM_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "postgresql://shell/db")
	asked := 0
	old := loadProxyEnv
	loadProxyEnv = func() ProxyEnv {
		asked++
		return ProxyEnv{Source: "the test proxy environment", vars: map[string]string{"DATABASE_URL": "postgresql://proxy/db"}}
	}
	t.Cleanup(func() { loadProxyEnv = old })
	body := "general_settings:\n  database_url: postgresql://real/db\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DatabaseURL()
	if got != "" || !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("DatabaseURL() = %q, %v; want \"\" and ErrNoDatabase", got, err)
	}
	if !strings.Contains(err.Error(), "registry is redirected") {
		t.Errorf("err = %q, want the redirected-registry reason", err)
	}
	if asked != 0 {
		t.Errorf("the proxy's environment was read %d times under the guard, want 0", asked)
	}

	t.Setenv("WT_LITELLM_CONFIG", p)
	if got, err := DatabaseURL(); err != nil || got != "postgresql://real/db" {
		t.Errorf("with config.yaml named: DatabaseURL() = %q, %v; want the file's value", got, err)
	}
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("WT_LITELLM_DATABASE_URL", "postgresql://explicit/db")
	if got, err := DatabaseURL(); err != nil || got != "postgresql://explicit/db" {
		t.Errorf("with the database named: DatabaseURL() = %q, %v; want the env value", got, err)
	}
}
