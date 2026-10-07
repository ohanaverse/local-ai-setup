package spend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// oddPassword is a password with every character that has gone wrong in a
// connection string: "/", "@", ":", "%", a space, a double quote and a
// letter outside ASCII.
const oddPassword = `FAKEp/@:% "é`

// TestQueryKeepsTheConnectionOutOfArgv verifies that nothing of the
// connection string — the whole string, the user, the password, the
// database name, the host — is among psql's arguments, for a URI and for a
// keyword string, and that the environment carries each value decoded: the
// URI's percent-escapes undone, the keyword string's quotes and backslashes
// removed. A connect_timeout in the string reaches psql once, in place of
// wt's 3 seconds. Arguments are readable by every user of the machine
// (`ps -ww`) while `wt stats` runs; that was the database password (#282). A
// value decoded wrongly is a login that fails for a password that is right;
// a timeout wt overrode would cut off a database that is slow to answer and
// was given longer.
func TestQueryKeepsTheConnectionOutOfArgv(t *testing.T) {
	cases := []struct {
		name, dsn string
		want      []string // the PG* variables, sorted
	}{
		{"a URI", "postgresql://FAKEuser:FAKEpass@db.example:5432/FAKEdb?sslmode=require",
			[]string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGPASSWORD=FAKEpass", "PGPORT=5432", "PGSSLMODE=require", "PGTZ=UTC", "PGUSER=FAKEuser"}},
		{"a URI with the short scheme", "postgres://FAKEuser:FAKEpass@db.example/FAKEdb",
			[]string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGPASSWORD=FAKEpass", "PGTZ=UTC", "PGUSER=FAKEuser"}},
		{"a URI with an escaped password", "postgresql://FAKE%20user:FAKEp%2F%40%3A%25%20%22%C3%A9@db.example:5432/FAKE%2Fdb",
			[]string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKE/db", "PGHOST=db.example", "PGPASSWORD=" + oddPassword, "PGPORT=5432", "PGTZ=UTC", "PGUSER=FAKE user"}},
		{"a URI with a raw quote and a raw é in the password", `postgresql://FAKEuser:FAKEp:%25"é@db.example/FAKEdb`,
			[]string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKEdb", "PGHOST=db.example", `PGPASSWORD=FAKEp:%"é`, "PGTZ=UTC", "PGUSER=FAKEuser"}},
		{"a URI with its own connect_timeout", "postgresql://db.example/FAKEdb?connect_timeout=30",
			[]string{"PGCONNECT_TIMEOUT=30", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGTZ=UTC"}},
		{"a keyword string", "host=db.example port=5432 user=FAKEuser password=FAKEpass dbname=FAKEdb sslmode=require",
			[]string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGPASSWORD=FAKEpass", "PGPORT=5432", "PGSSLMODE=require", "PGTZ=UTC", "PGUSER=FAKEuser"}},
		{"a keyword string with a quoted password", `host=db.example user='FAKE user' password='FAKEp/@:% "é' dbname=FAKEdb`,
			[]string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGPASSWORD=" + oddPassword, "PGTZ=UTC", "PGUSER=FAKE user"}},
		{"a keyword string with backslashes", `host=db.example password=FAKEp/@:%\ "é\'\\ dbname = 'FAKE\'d\\b'`,
			[]string{"PGCONNECT_TIMEOUT=3", `PGDATABASE=FAKE'd\b`, "PGHOST=db.example", "PGPASSWORD=" + oddPassword + `'\`, "PGTZ=UTC"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			call := stubPsql(t, "[]", "", 0)
			if _, err := Query(context.Background(), c.dsn, winStart, winEnd); err != nil {
				t.Fatalf("Query: %v", err)
			}
			wantArgs := []string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-c", querySQL(winStart, winEnd)}
			if !slices.Equal(call.args, wantArgs) {
				t.Errorf("args = %q\nwant %q", call.args, wantArgs)
			}
			argv := call.bin + "\n" + strings.Join(call.args, "\n")
			for _, piece := range []string{c.dsn, "FAKE", "db.example", "5432", "postgres", "password", oddPassword, "-d"} {
				if strings.Contains(argv, piece) {
					t.Errorf("psql's arguments hold %q from the connection string: %q", piece, call.args)
				}
			}
			if got := libpqVars(call.env); !slices.Equal(got, c.want) {
				t.Errorf("libpq variables =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// TestQueryClearsInheritedLibpqVariables verifies psql's environment has no
// PG* variable from wt's own: only the ones the connection string sets,
// and wt's timeout and time zone. With -d gone, libpq fills everything the
// string leaves out from its environment, so a PGHOST or PGSERVICE left in
// the user's shell would send the spend query to another server, a
// PGSSLMODE=disable would drop the encryption, and a PGUSER or PGPASSWORD
// would log in as someone else. The rest of the environment (PATH, HOME —
// where ~/.pgpass is found) is kept.
func TestQueryClearsInheritedLibpqVariables(t *testing.T) {
	for _, kv := range []string{
		"PGHOST=FAKEstray.example", "PGHOSTADDR=10.9.9.9", "PGPORT=9999", "PGDATABASE=FAKEstraydb",
		"PGUSER=FAKEstrayuser", "PGPASSWORD=FAKEstraypw", "PGPASSFILE=/FAKE/pgpass", "PGSERVICE=FAKEsvc",
		"PGSERVICEFILE=/FAKE/pg_service.conf", "PGSYSCONFDIR=/FAKE/etc", "PGSSLMODE=disable", "PGOPTIONS=-c FAKE=1",
		"PGCONNECT_TIMEOUT=99", "PGTZ=FAKE/Zone", "PGCLIENTENCODING=LATIN1", "PGREQUIRESSL=0",
		"WT_SPEND_TEST_KEPT=FAKEkept",
	} {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	call := stubPsql(t, "[]", "", 0)
	if _, err := Query(context.Background(), "postgresql://db.example/FAKEdb", winStart, winEnd); err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGTZ=UTC"}
	if got := libpqVars(call.env); !slices.Equal(got, want) {
		t.Errorf("libpq variables =\n%q\nwant only\n%q", got, want)
	}
	if !slices.Contains(call.env, "WT_SPEND_TEST_KEPT=FAKEkept") {
		t.Error("a variable that is not libpq's was dropped from psql's environment")
	}
	seen := map[string]int{}
	for _, kv := range call.env {
		k, _, _ := strings.Cut(kv, "=")
		seen[k]++
	}
	for k, n := range seen {
		if n > 1 && strings.HasPrefix(k, "PG") {
			t.Errorf("%s is set %d times in psql's environment", k, n)
		}
	}
}

// TestQueryRefusesWhatItCannotCarryOver verifies each connection string wt
// cannot hand to psql through the environment exactly as -d would have is
// refused before psql is looked for or run, as ErrConnectionString with one
// of two fixed messages that quote nothing from the string. Dropping the
// option instead would be a quiet change of behaviour: a connection without
// the keepalive, the service, or the authentication requirement the string
// asked for. Guessing at a string libpq reads differently could put a piece
// of the password in PGHOST and then in the "cannot reach" note.
func TestQueryRefusesWhatItCannotCarryOver(t *testing.T) {
	const unreadable = "the LiteLLM database connection string cannot be handed to psql: it is not written in a form wt can read the way psql would"
	const unsupported = "the LiteLLM database connection string cannot be handed to psql: it sets an option wt cannot pass in psql's environment"
	const uri = "postgresql://FAKEuser:FAKEpass@db.example:5432/FAKEdb"
	cases := []struct{ name, dsn, want string }{
		// Options with no environment form.
		{"a URI option with no variable", uri + "?keepalives=1", unsupported},
		{"sslpassword", uri + "?sslpassword=FAKEsslpw", unsupported},
		{"a keyword option with no variable", "host=db.example user=FAKEuser password=FAKEpass tcp_user_timeout=5", unsupported},
		{"an option libpq does not know (Prisma's schema)", uri + "?schema=FAKEschema", unsupported},
		{"an unknown keyword", "host=db.example FAKEkey=FAKEvalue", unsupported},
		{"a keyword with no name", "host=db.example =FAKEvalue", unsupported},
		{"an option only libpq 16 and later know", uri + "?require_auth=scram-sha-256", unsupported},
		{"a service in a URI", uri + "?service=FAKEsvc", unsupported},
		{"a service in a keyword string", "service=FAKEsvc password=FAKEpass", unsupported},
		{"ssl with a value other than true", uri + "?ssl=FAKEyes", unsupported},
		// Strings libpq refuses, or that two libpq versions read differently.
		{"a bad percent escape", "postgresql://FAKEuser:FAKEpa%zzword@db.example/FAKEdb", unreadable},
		{"a truncated percent escape", "postgresql://FAKEuser:FAKEpass@db.example/FAKEdb%2", unreadable},
		{"an escaped NUL", "postgresql://FAKEuser:FAKEpa%00ss@db.example/FAKEdb", unreadable},
		// libpq fails the whole string at the first %00, also where a later
		// parameter replaces the value that held it.
		{"an escaped NUL in a user a parameter replaces", "postgresql://FAKEuser%00@db.example/FAKEdb?user=FAKEother", unreadable},
		{"an escaped NUL in a password a parameter replaces", "postgresql://FAKEuser:FAKEpa%00ss@db.example/FAKEdb?password=FAKEpass", unreadable},
		{"an escaped NUL in a host a parameter replaces", "postgresql://db%00.example/FAKEdb?host=db.example", unreadable},
		{"an escaped NUL in a port a parameter replaces", "postgresql://db.example:54%0032/FAKEdb?port=5432", unreadable},
		{"an escaped NUL in a database name a parameter replaces", "postgresql://db.example/FAKE%00db?dbname=FAKEdb", unreadable},
		{"an escaped NUL in a parameter given again", uri + "?sslmode=FAKE%00&sslmode=require", unreadable},
		{"an escaped NUL in a parameter name", uri + "?ssl%00mode=require", unreadable},
		{"a space in a URI", "postgresql://FAKEuser:FAKEpa ss@db.example/FAKEdb", unreadable},
		{"a space after a URI", uri + " ", unreadable},
		{"a newline in a URI", "postgresql://FAKEuser:FAKEpa\nss@db.example/FAKEdb", unreadable},
		{"a URI parameter with no value", uri + "?sslmode", unreadable},
		{"a URI parameter with two separators", uri + "?sslmode=FAKEa=b", unreadable},
		{"an empty URI parameter", uri + "?&sslmode=require", unreadable},
		{"an IPv6 address with no closing bracket", "postgresql://FAKEuser:FAKEpass@[::1/FAKEdb", unreadable},
		{"an empty IPv6 address", "postgresql://FAKEuser:FAKEpass@[]/FAKEdb", unreadable},
		{"text after an IPv6 address", "postgresql://FAKEuser:FAKEpass@[::1]FAKE/FAKEdb", unreadable},
		{"a keyword value cut by a space", "host=db.example user=FAKEuser password=FAKEabc FAKEdef", unreadable},
		{"a quote that never closes", "host=db.example password='FAKEpass", unreadable},
		{"a quote that ends in a backslash", `host=db.example password='FAKEpass\`, unreadable},
		{"an unquoted à, which ends in the byte macOS calls a space", "host=db.example password=FAKEàpass", unreadable},
		{"an unquoted no-break space", "host=db.example password=FAKE pass", unreadable},
		{"an unquoted byte 0x85", "host=db.example password=FAKE\u0085pass", unreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			call := stubPsql(t, "[]", "", 0)
			looked := 0
			lookPath = func(string) (string, error) { looked++; return "/fake/psql", nil }
			_, err := Query(context.Background(), c.dsn, winStart, winEnd)
			if !errors.Is(err, ErrConnectionString) {
				t.Fatalf("err = %v, want ErrConnectionString", err)
			}
			if err.Error() != c.want {
				t.Errorf("err = %q\nwant  %q", err, c.want)
			}
			if looked != 0 || call.n != 0 {
				t.Errorf("psql was looked up %d times and run %d times, want neither", looked, call.n)
			}
		})
	}
}

// TestConnEnvReadsAStringAsLibpqDoes pins how a connection string becomes
// environment variables in the cases where "the obvious reading" and
// libpq's differ, or where the string could be taken two ways. Every
// expectation is what libpq 16's own PQconninfoParse answers for that
// string (the option values; wt adds the variable names). psql now connects
// to what wt read, so a reading that differs from libpq's is a spend query
// sent to a host, or with a user, the connection string does not name.
func TestConnEnvReadsAStringAsLibpqDoes(t *testing.T) {
	cases := []struct {
		name, dsn string
		want      []string
	}{
		{"no user and no database", "postgresql://db.example", []string{"PGHOST=db.example"}},
		{"nothing but the scheme", "postgresql://", nil},
		{"a database and no host: the default socket", "postgresql:///FAKEdb", []string{"PGDATABASE=FAKEdb"}},
		{"a user and no password", "postgresql://FAKEuser@db.example", []string{"PGHOST=db.example", "PGUSER=FAKEuser"}},
		{"an empty password is no password", "postgresql://FAKEuser:@db.example", []string{"PGHOST=db.example", "PGUSER=FAKEuser"}},
		{"the password runs to the @, colons included", "postgresql://FAKEuser:FAKEa:b@db.example",
			[]string{"PGHOST=db.example", "PGPASSWORD=FAKEa:b", "PGUSER=FAKEuser"}},
		{"an IPv6 address", "postgresql://FAKEuser@[::1]:5433/FAKEdb",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=::1", "PGPORT=5433", "PGUSER=FAKEuser"}},
		{"a socket directory", "postgresql://FAKEuser@%2Fvar%2Frun%2Fpostgresql/FAKEdb",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=/var/run/postgresql", "PGUSER=FAKEuser"}},
		{"an escaped port", "postgresql://db.example:54%332/FAKEdb",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=db.example", "PGPORT=5432"}},
		{"several hosts", "postgresql://db-a.example:5432,db-b.example:6432/FAKEdb?target_session_attrs=read-write",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=db-a.example,db-b.example", "PGPORT=5432,6432", "PGTARGETSESSIONATTRS=read-write"}},
		{"several hosts, one without a port", "postgresql://db-a.example,db-b.example:6432/FAKEdb",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=db-a.example,db-b.example", "PGPORT=,6432"}},
		{"parameters override the address part", "postgresql://FAKEuser:FAKEpass@db.example:5432/FAKEdb?host=other.example&port=6432&user=FAKEu2&password=FAKEp2&dbname=FAKEd2",
			[]string{"PGDATABASE=FAKEd2", "PGHOST=other.example", "PGPASSWORD=FAKEp2", "PGPORT=6432", "PGUSER=FAKEu2"}},
		{"a parameter given twice: the later one", "postgresql://db.example/FAKEdb?sslmode=disable&sslmode=verify-full",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=db.example", "PGSSLMODE=verify-full"}},
		{"ssl=true is sslmode=require", "postgresql://db.example/FAKEdb?ssl=true",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=db.example", "PGSSLMODE=require"}},
		{"a parameter name is decoded too", "postgresql://db.example/FAKEdb?ssl%6dode=require",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=db.example", "PGSSLMODE=require"}},
		{"a plus is a plus, an escape is decoded once", "postgresql://db.example/FAKEdb?application_name=FAKEa+b%2520&options=-c%20search_path%3DFAKEs",
			[]string{"PGAPPNAME=FAKEa+b%20", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGOPTIONS=-c search_path=FAKEs"}},
		{"no fragment: a # belongs to the database name", "postgresql://db.example/FAKEdb#x",
			[]string{"PGDATABASE=FAKEdb#x", "PGHOST=db.example"}},
		{"connect_timeout in the string replaces wt's", "postgresql://db.example/FAKEdb?connect_timeout=30",
			[]string{"PGCONNECT_TIMEOUT=30", "PGDATABASE=FAKEdb", "PGHOST=db.example"}},
		{"file options", "postgresql://db.example/FAKEdb?passfile=%2FFAKE%2Fpgpass&sslrootcert=%2FFAKE%2Fca.pem&channel_binding=require",
			[]string{"PGCHANNELBINDING=require", "PGDATABASE=FAKEdb", "PGHOST=db.example", "PGPASSFILE=/FAKE/pgpass", "PGSSLROOTCERT=/FAKE/ca.pem"}},
		// libpq's mis-splits, kept as they are: psql then fails on them just
		// as it did with -d, and plainAddress keeps the pieces out of the note.
		{"a slash in the password: no user, the user name is the host", "postgresql://FAKEuser:FAKEabc/def@db.example:5432/FAKEdb",
			[]string{"PGDATABASE=def@db.example:5432/FAKEdb", "PGHOST=FAKEuser", "PGPORT=FAKEabc"}},
		{"an @ in the password: its tail is the host", "postgresql://FAKEuser:FAKEp@ss@db.example:5432/FAKEdb",
			[]string{"PGDATABASE=FAKEdb", "PGHOST=ss@db.example", "PGPASSWORD=FAKEp", "PGPORT=5432", "PGUSER=FAKEuser"}},
		{"an @ in a parameter and no path: the host is the user", "postgresql://db.example?user=FAKEa@FAKEb",
			[]string{"PGHOST=FAKEb", "PGUSER=db.example?user=FAKEa"}},

		{"blanks around the =", "host = db.example  port= 5432\tuser =FAKEuser",
			[]string{"PGHOST=db.example", "PGPORT=5432", "PGUSER=FAKEuser"}},
		{"a quoted value ends at its quote", "password='FAKEpass'host=db.example",
			[]string{"PGHOST=db.example", "PGPASSWORD=FAKEpass"}},
		{"a keyword given twice: the later one", "host=a.example host=b.example", []string{"PGHOST=b.example"}},
		{"an empty value", "host=db.example password=", []string{"PGHOST=db.example", "PGPASSWORD="}},
		{"a value with an =", "host=db.example password=FAKEa=b", []string{"PGHOST=db.example", "PGPASSWORD=FAKEa=b"}},
		{"a trailing backslash is dropped", `host=db.example password=FAKEpass\`, []string{"PGHOST=db.example", "PGPASSWORD=FAKEpass"}},
		{"a quoted à", "host=db.example password='FAKEàpass'", []string{"PGHOST=db.example", "PGPASSWORD=FAKEàpass"}},
		{"a URI as the dbname of a keyword string is a name", "host=db.example dbname=postgresql://FAKEuser:FAKEpass@other.example/FAKEdb",
			[]string{"PGDATABASE=postgresql://FAKEuser:FAKEpass@other.example/FAKEdb", "PGHOST=db.example"}},
		{"a host list in a keyword string", "host=db-a.example,db-b.example port=5432,6432",
			[]string{"PGHOST=db-a.example,db-b.example", "PGPORT=5432,6432"}},
		// Not from PQconninfoParse: a string with no "=" and no URI scheme is
		// what -d takes as a database name.
		{"a bare database name", "FAKEdb", []string{"PGDATABASE=FAKEdb"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := connEnv(c.dsn)
			if err != nil {
				t.Fatalf("connEnv: %v", err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("connEnv =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// TestEveryPassedOptionHasItsOwnVariable verifies the option table names a
// distinct PG* variable for each option and leaves out the ones that cannot
// be passed faithfully. Two options sharing a variable would let one
// overwrite the other in psql's environment; "service" as PGSERVICE would
// let the service file override the string's own host.
func TestEveryPassedOptionHasItsOwnVariable(t *testing.T) {
	seen := map[string]string{}
	for opt, name := range envFor {
		if !strings.HasPrefix(name, "PG") {
			t.Errorf("%s maps to %s, which psqlEnv would not clear from an inherited environment", opt, name)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("%s and %s both map to %s", opt, other, name)
		}
		seen[name] = opt
	}
	for _, opt := range []string{"service", "sslpassword", "keepalives", "require_auth", "replication"} {
		if _, ok := envFor[opt]; ok {
			t.Errorf("%s is passed in the environment; it has no faithful form there", opt)
		}
	}
}

// TestQueryHandsARealPsqlNothingOnItsCommandLine runs Query against a
// stand-in psql found on PATH — a shell script that records its arguments
// and its environment — with libpq variables set in wt's own environment,
// and checks exactly what the real psql would receive: the fixed options
// and the statement as arguments, the connection in PG* variables, and none
// of the inherited ones. The stubbed tests see what Query asked for; this
// one sees what a child process got, which is what `ps` and libpq see.
func TestQueryHandsARealPsqlNothingOnItsCommandLine(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "psql")
	argvFile, envFile := filepath.Join(dir, "argv"), filepath.Join(dir, "env")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+
		`printf '%s\n' "$@" > "`+argvFile+`"`+"\n"+
		`env > "`+envFile+`"`+"\n"+
		"echo '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The stand-in's directory comes first, so it is the psql that is found;
	// the run is refused below should anything else be.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("PGHOST", "FAKEstray.example")
	t.Setenv("PGSERVICE", "FAKEsvc")
	t.Setenv("PGSSLMODE", "disable")
	oldLook, oldRun := lookPath, runPsql
	lookPath = realLookPath
	runPsql = func(ctx context.Context, bin string, args, env []string) ([]byte, []byte, int, error) {
		if bin != script {
			return nil, nil, 0, errors.New("found a psql that is not the stand-in: " + bin)
		}
		return realRunPsql(ctx, bin, args, env)
	}
	t.Cleanup(func() { lookPath, runPsql = oldLook, oldRun })

	const dsn = "postgresql://FAKEuser:FAKEpass@127.0.0.1:1/FAKEdb"
	if _, err := Query(context.Background(), dsn, winStart, winEnd); err != nil {
		t.Fatalf("Query: %v", err)
	}

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("the stand-in did not record its arguments: %v", err)
	}
	wantArgv := strings.Join([]string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-c", querySQL(winStart, winEnd)}, "\n") + "\n"
	if string(argv) != wantArgv {
		t.Errorf("the child's arguments =\n%s\nwant\n%s", argv, wantArgv)
	}
	for _, piece := range []string{"FAKE", "127.0.0.1", "postgresql://", "-d"} {
		if strings.Contains(string(argv), piece) {
			t.Errorf("the child's arguments hold %q", piece)
		}
	}

	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("the stand-in did not record its environment: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(env)), "\n")
	want := []string{"PGCONNECT_TIMEOUT=3", "PGDATABASE=FAKEdb", "PGHOST=127.0.0.1", "PGPASSWORD=FAKEpass", "PGPORT=1", "PGTZ=UTC", "PGUSER=FAKEuser"}
	if got := libpqVars(lines); !slices.Equal(got, want) {
		t.Errorf("the child's libpq variables =\n%q\nwant\n%q", got, want)
	}
	if !slices.Contains(lines, "LC_MESSAGES=C") {
		t.Error("the child's environment lacks LC_MESSAGES=C")
	}
}
