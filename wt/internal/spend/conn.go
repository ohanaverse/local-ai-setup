package spend

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// How the connection reaches psql — the decision behind this file (#282).
//
// psql used to get the connection string as `-d <string>`. A process's
// arguments are readable by every user of the machine (`ps -ww`), and the
// string normally holds the database password. So psql now gets no -d at
// all: wt reads the string into its options and hands each one over in the
// libpq environment variable for it (PGHOST, PGPORT, PGUSER, PGPASSWORD,
// PGDATABASE, PGSSLMODE, …). A process's environment is readable only by
// the same user and by root — on macOS `ps -E` shows it for one's own
// processes only (checked), on Linux /proc/<pid>/environ is readable by
// its owner only — so the password is no longer shown to the machine's
// other users. It is not hidden from the user's own processes.
//
// The alternatives, and why not:
//
//   - PGDATABASE cannot carry the string. libpq expands a connection string
//     only in the dbname it is given directly (-d); one in PGDATABASE is
//     taken as a database name, and psql connects to the default socket.
//     (Checked against psql 16.)
//   - A service file (PGSERVICEFILE + PGSERVICE) needs the same reading of
//     the string into options, and then also a file holding the password,
//     its permissions, and its removal on every way out. More to get wrong,
//     for one gain — options with no environment variable — that nothing
//     has asked for.
//
// Reading the string here means wt and libpq must agree on what it says, so
// the two readers below follow libpq's own (conninfo_parse and
// conninfo_uri_parse_options in fe-connect.c) step for step, including the
// way it mis-splits a URI whose password holds an unencoded "/" or "@" —
// net/url splits those differently, and rejects a raw space or quote in a
// password that libpq takes. Only the percent-decoding is net/url's. Where
// the two could still disagree, or an option has no faithful environment
// form, the string is refused (ErrConnectionString) and psql is not run:
//
//   - an option that has no environment variable (keepalives, sslpassword,
//     …), one libpq does not know, or one newer than every supported libpq
//     has (require_auth, …): an older psql would ignore the variable, where
//     it would have refused the option in the string;
//   - service: in the string, the string's other options override the
//     service file's; as PGSERVICE the file would override them;
//   - a URI with a space or control character (libpq 16 takes them into
//     the value; later releases are stricter), a bad percent-escape, or
//     %00;
//   - a keyword string libpq cannot read, or one with a byte 0x85 or 0xA0
//     outside quotes: C's isspace() says yes or no to those by locale (yes
//     on macOS), which decides where a value ends. Both occur inside UTF-8
//     letters ("à" ends in 0xA0), so such a value has to be quoted.
//
// A host list (host1,host2) needs nothing special: libpq's URI reader only
// joins the names and ports with commas, and PGHOST and PGPORT take the
// same lists.

// ErrConnectionString: the connection string cannot be handed to psql in
// its environment. psql is not run. The error says which of two fixed
// reasons applies and quotes nothing from the string.
var ErrConnectionString = errors.New("the LiteLLM database connection string cannot be handed to psql")

var (
	// errUnreadable: libpq would not read the string, or might read it
	// differently from wt.
	errUnreadable = fmt.Errorf("%w: it is not written in a form wt can read the way psql would", ErrConnectionString)
	// errUnsupported: the string sets an option that is not in envFor.
	errUnsupported = fmt.Errorf("%w: it sets an option wt cannot pass in psql's environment", ErrConnectionString)
)

// envFor is the libpq environment variable for each connection option wt
// passes on. It is every option that has a variable in all libpq releases
// still supported (14 and later), except service (see the note above).
var envFor = map[string]string{
	"host":                     "PGHOST",
	"hostaddr":                 "PGHOSTADDR",
	"port":                     "PGPORT",
	"dbname":                   "PGDATABASE",
	"user":                     "PGUSER",
	"password":                 "PGPASSWORD",
	"passfile":                 "PGPASSFILE",
	"connect_timeout":          "PGCONNECT_TIMEOUT",
	"client_encoding":          "PGCLIENTENCODING",
	"options":                  "PGOPTIONS",
	"application_name":         "PGAPPNAME",
	"channel_binding":          "PGCHANNELBINDING",
	"sslmode":                  "PGSSLMODE",
	"sslcompression":           "PGSSLCOMPRESSION",
	"sslcert":                  "PGSSLCERT",
	"sslkey":                   "PGSSLKEY",
	"sslrootcert":              "PGSSLROOTCERT",
	"sslcrl":                   "PGSSLCRL",
	"sslcrldir":                "PGSSLCRLDIR",
	"sslsni":                   "PGSSLSNI",
	"ssl_min_protocol_version": "PGSSLMINPROTOCOLVERSION",
	"ssl_max_protocol_version": "PGSSLMAXPROTOCOLVERSION",
	"requirepeer":              "PGREQUIREPEER",
	"gssencmode":               "PGGSSENCMODE",
	"krbsrvname":               "PGKRBSRVNAME",
	"gsslib":                   "PGGSSLIB",
	"target_session_attrs":     "PGTARGETSESSIONATTRS",
}

// connEnv turns a connection string into the NAME=value environment entries
// that make psql, run with no -d, connect exactly as `-d dsn` would. The
// entries are sorted by name. A string that cannot be carried over
// faithfully is refused with ErrConnectionString.
func connEnv(dsn string) ([]string, error) {
	opts, err := connOptions(dsn)
	if err != nil {
		return nil, err
	}
	env := make([]string, 0, len(opts))
	for k, v := range opts {
		name, ok := envFor[k]
		if !ok {
			return nil, errUnsupported
		}
		// An environment entry cannot hold a NUL, and libpq refuses %00.
		if strings.ContainsRune(v, 0) {
			return nil, errUnreadable
		}
		env = append(env, name+"="+v)
	}
	sort.Strings(env)
	return env, nil
}

// connOptions reads dsn into libpq's option names and their values, as
// libpq does for a dbname it expands: a URI, else a keyword string if it
// has an "=", else the name of a database. Where an option is given twice
// the later one counts.
func connOptions(dsn string) (map[string]string, error) {
	for _, prefix := range []string{"postgresql://", "postgres://"} {
		if rest, ok := strings.CutPrefix(dsn, prefix); ok {
			return uriOptions(rest)
		}
	}
	if !strings.Contains(dsn, "=") {
		return map[string]string{"dbname": dsn}, nil
	}
	return keywordOptions(dsn)
}

// cutAny splits s before the first byte that is in stops; without one, it
// returns all of s and "".
func cutAny(s, stops string) (before, rest string) {
	if i := strings.IndexAny(s, stops); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// uriOptions reads what follows the scheme of a connection URI:
//
//	[user[:password]@][host][:port][,host[:port]…][/dbname][?name=value&…]
//
// in libpq's order of decisions. The user part ends at the first "@" only
// if no "/" comes before it; a host ends at the first of ":/?,", or is
// bracketed; names and values are percent-decoded after the split, and "+"
// is not a space.
func uriOptions(p string) (map[string]string, error) {
	for i := 0; i < len(p); i++ {
		if p[i] <= ' ' || p[i] == 0x7f {
			return nil, errUnreadable
		}
	}
	opts := map[string]string{}
	var bad bool
	decode := func(raw string) string {
		v, err := url.PathUnescape(raw)
		if err != nil {
			bad = true
		}
		return v
	}

	if i := strings.IndexAny(p, "@/"); i >= 0 && p[i] == '@' {
		user, password, hasPassword := strings.Cut(p[:i], ":")
		p = p[i+1:]
		if user != "" {
			opts["user"] = decode(user)
		}
		if hasPassword && password != "" {
			opts["password"] = decode(password)
		}
	}

	var hosts, ports strings.Builder
	for {
		var host string
		if strings.HasPrefix(p, "[") {
			end := strings.IndexByte(p, ']')
			if end <= 1 {
				return nil, errUnreadable // no "]", or "[]"
			}
			host, p = p[1:end], p[end+1:]
			if p != "" && !strings.ContainsRune(":/?,", rune(p[0])) {
				return nil, errUnreadable
			}
		} else {
			host, p = cutAny(p, ":/?,")
		}
		hosts.WriteString(host)
		if strings.HasPrefix(p, ":") {
			var port string
			port, p = cutAny(p[1:], "/?,")
			ports.WriteString(port)
		}
		if !strings.HasPrefix(p, ",") {
			break
		}
		p = p[1:]
		hosts.WriteByte(',')
		ports.WriteByte(',')
	}
	if hosts.Len() > 0 {
		opts["host"] = decode(hosts.String())
	}
	if ports.Len() > 0 {
		opts["port"] = decode(ports.String())
	}

	if strings.HasPrefix(p, "/") {
		var dbname string
		dbname, p = cutAny(p[1:], "?")
		if dbname != "" {
			opts["dbname"] = decode(dbname)
		}
	}

	p = strings.TrimPrefix(p, "?")
	for p != "" {
		var pair string
		pair, p, _ = strings.Cut(p, "&")
		k, v, ok := strings.Cut(pair, "=")
		if !ok || strings.Contains(v, "=") {
			return nil, errUnreadable
		}
		k, v = decode(k), decode(v)
		if k == "ssl" && v == "true" {
			k, v = "sslmode", "require" // libpq's one alias
		}
		opts[k] = v
	}
	if bad {
		return nil, errUnreadable
	}
	return opts, nil
}

// keywordOptions reads a keyword string (name=value name='quoted value' …)
// by libpq's grammar: blanks separate, may surround the "=", and end an
// unquoted value; a single-quoted value runs to the closing quote; in
// either, a backslash takes the next byte as it is.
func keywordOptions(s string) (map[string]string, error) {
	opts := map[string]string{}
	var bad bool
	// space is C's isspace() in the "C" locale. The two bytes other locales
	// add are refused wherever libpq would ask.
	space := func(c byte) bool {
		if c == 0x85 || c == 0xa0 {
			bad = true
		}
		return c == ' ' || (c >= '\t' && c <= '\r')
	}
	i := 0
	skipBlanks := func() {
		for i < len(s) && space(s[i]) {
			i++
		}
	}
	for {
		skipBlanks()
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && s[i] != '=' && !space(s[i]) {
			i++
		}
		name := s[start:i]
		skipBlanks()
		if i >= len(s) || s[i] != '=' {
			return nil, errUnreadable
		}
		i++
		skipBlanks()

		var val []byte
		quoted := i < len(s) && s[i] == '\''
		if quoted {
			i++
		}
		for {
			if i >= len(s) {
				if quoted {
					return nil, errUnreadable // no closing quote
				}
				break
			}
			c := s[i]
			i++
			if c == '\\' {
				if i < len(s) {
					val = append(val, s[i])
					i++
				}
				continue
			}
			if quoted && c == '\'' || !quoted && space(c) {
				break
			}
			val = append(val, c)
		}
		opts[name] = string(val)
	}
	if bad {
		return nil, errUnreadable
	}
	return opts, nil
}

// psqlEnv is the environment psql runs in: base without any libpq variable
// (a PGHOST, PGSERVICE or PGSSLMODE left in the user's shell would redirect
// or weaken a connection the string does not fully spell out), then wt's
// own settings, then the connection. A connect_timeout in the string wins
// over wt's, as it did when the string was psql's argument; the deadline
// bounds the run either way.
func psqlEnv(base, conn []string) []string {
	env := make([]string, 0, len(base)+len(conn)+3)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "PG") {
			env = append(env, kv)
		}
	}
	// LC_MESSAGES=C: unreachable reads libpq's English connect-failure line,
	// and under a translated one it can only withhold the address and the
	// reason. (An LC_ALL in the user's environment still wins; the note is
	// then the withheld one, never a leak.)
	env = append(env, "PGTZ=UTC", "LC_MESSAGES=C")
	if !hasVar(conn, "PGCONNECT_TIMEOUT") {
		env = append(env, "PGCONNECT_TIMEOUT="+connectTimeout)
	}
	return append(env, conn...)
}

// hasVar reports whether env has an entry for name.
func hasVar(env []string, name string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}
