package spend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// psqlCall records what a stubbed psql was run with.
type psqlCall struct {
	bin  string
	args []string
	env  []string
	n    int
}

// stubPsql makes psql "installed" at /fake/psql and answers every run with
// the given output and exit status. Both seams are restored on cleanup.
func stubPsql(t *testing.T, stdout, stderr string, exit int) *psqlCall {
	t.Helper()
	call := &psqlCall{}
	oldLook, oldRun := lookPath, runPsql
	lookPath = func(string) (string, error) { return "/fake/psql", nil }
	runPsql = func(_ context.Context, bin string, args, env []string) ([]byte, []byte, int, error) {
		call.bin, call.args, call.env = bin, args, env
		call.n++
		return []byte(stdout), []byte(stderr), exit, nil
	}
	t.Cleanup(func() { lookPath, runPsql = oldLook, oldRun })
	return call
}

// fakePsqlBinary writes an executable shell script and points lookPath at
// it, with the real process runner restored. The script stands in for
// psql: no test runs the real one.
func fakePsqlBinary(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "psql")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldLook, oldRun := lookPath, runPsql
	lookPath = func(string) (string, error) { return p, nil }
	runPsql = realRunPsql
	t.Cleanup(func() { lookPath, runPsql = oldLook, oldRun })
	return p
}

var (
	winStart = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	winEnd   = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
)

// TestQueryParsesTheAggregate verifies the JSON document psql prints is
// decoded into rows: json_agg spreads the array over several lines, token
// sums exceed 32 bits, and the group with an empty model becomes the
// Unattributed count instead of a row. These are the numbers in `wt
// stats`'s REQUESTS, PROMPT, COMPLETION and SPEND columns.
func TestQueryParsesTheAggregate(t *testing.T) {
	stubPsql(t, `[{"model":"","requests":3,"prompt_tokens":0,"completion_tokens":0,"spend":0}, 
 {"model":"ollama/gemma4:9b","requests":4,"prompt_tokens":152,"completion_tokens":630,"spend":0}, 
 {"model":"openrouter/qwen/qwen3.8-27b","requests":5,"prompt_tokens":4294967294,"completion_tokens":3135,"spend":0.0085}]
`, "", 0)

	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []Row{
		{Model: "ollama/gemma4:9b", Requests: 4, PromptTokens: 152, CompletionTokens: 630},
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 4294967294, CompletionTokens: 3135, Spend: 0.0085},
	}
	if !slices.Equal(got.Rows, want) {
		t.Errorf("Rows = %+v, want %+v", got.Rows, want)
	}
	if got.Unattributed != 3 {
		t.Errorf("Unattributed = %d, want 3 (the empty-model group's requests)", got.Unattributed)
	}
}

// TestQueryKeepsOddModelIDsWhole verifies a model group with characters a
// delimited format would choke on — a pipe (psql's own field separator), a
// quote, a comma, a space, non-ASCII — comes back exactly as logged. This
// is why the query returns JSON: the proxy logs whatever public model name
// a client was routed by, and wt joins on the exact string.
func TestQueryKeepsOddModelIDsWhole(t *testing.T) {
	stubPsql(t, `[{"model":"a|b, \"c\" d/é","requests":1,"prompt_tokens":0,"completion_tokens":0,"spend":0}]`, "", 0)
	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Model != `a|b, "c" d/é` {
		t.Fatalf("Query = %+v, %v; want the one model id unchanged", got, err)
	}
}

// TestQueryEmptyWindow verifies a window with no logged requests — psql
// prints "[]" — is an empty result and not an error. A proxy that logged
// nothing this week is a normal state, and `wt stats` must show zeros for
// it rather than "spend unavailable".
func TestQueryEmptyWindow(t *testing.T) {
	stubPsql(t, "[]\n", "", 0)
	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil || len(got.Rows) != 0 || got.Unattributed != 0 {
		t.Fatalf("Query = %+v, %v; want an empty result and no error", got, err)
	}
}

// TestQueryInvocation pins how psql is run, which is the spec's wording:
// `psql -X -w -q -At -v ON_ERROR_STOP=1`, the connection string after -d
// (libpq does not expand a URI given in PGDATABASE), the statement after
// -c, and PGCONNECT_TIMEOUT=3 in the environment. -X keeps a ~/.psqlrc
// from changing the output format; -w keeps psql from ever stopping at a
// password prompt inside `wt stats`. LC_MESSAGES=C asks libpq for the
// English connect-failure line the error mapping reads, so a translated
// locale does not cost the note its address and reason.
func TestQueryInvocation(t *testing.T) {
	call := stubPsql(t, "[]", "", 0)
	if _, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd); err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-d", "postgresql://h/db", "-c", querySQL(winStart, winEnd)}
	if call.bin != "/fake/psql" || !slices.Equal(call.args, want) {
		t.Errorf("ran %s %q\nwant /fake/psql %q", call.bin, call.args, want)
	}
	for _, kv := range []string{"PGCONNECT_TIMEOUT=3", "PGTZ=UTC", "LC_MESSAGES=C"} {
		if !slices.Contains(call.env, kv) {
			t.Errorf("env lacks %s", kv)
		}
	}
	if call.n != 1 {
		t.Errorf("psql ran %d times, want 1 (one aggregated query)", call.n)
	}
}

// TestQuerySQL pins the statement itself: one aggregate over
// "LiteLLM_SpendLogs" grouped by model_group, the columns modelman's report
// summed, and a window written as zone-less UTC literals whatever zone the
// caller's times are in. The window is the port of modelman's
// `"startTime" >= start AND "startTime" <= end`. A times-in-local-zone
// literal here would shift the window by the machine's UTC offset.
func TestQuerySQL(t *testing.T) {
	// A fixed zone needs no tzdata, so this never skips.
	est := time.FixedZone("EST", -5*3600)
	got := querySQL(winStart.In(est), winEnd.In(est))
	want := `SELECT coalesce(json_agg(t), '[]'::json) FROM (` +
		`SELECT coalesce(model_group, '') AS model, count(*) AS requests, ` +
		`coalesce(sum(prompt_tokens), 0) AS prompt_tokens, ` +
		`coalesce(sum(completion_tokens), 0) AS completion_tokens, ` +
		`coalesce(sum(spend), 0) AS spend ` +
		`FROM "LiteLLM_SpendLogs" ` +
		`WHERE "startTime" >= timestamp '2026-09-07 12:00:00.000000' ` +
		`AND "startTime" <= timestamp '2026-10-07 12:00:00.000000' ` +
		`GROUP BY 1 ORDER BY 1) t`
	if got != want {
		t.Errorf("querySQL =\n%s\nwant\n%s", got, want)
	}
}

// TestQueryErrorMapping verifies each way psql fails becomes the right
// typed error with a one-line reason: no binary, a refused connection
// (exit 2, with the stderr text psql 16 really prints), a login the server
// refused, a host name that does not resolve, a SQL error, and output that
// is not JSON. `wt stats` prints the message as its one note, and the type
// tells a caller "not reachable" from "broken". An unreachable database is
// named by host and port (or socket path) so a wrong address can be seen;
// the user and the database name are blanked, and a host or port that is
// not shaped like one is left out.
//
// libpq prints one line per address it tried. The last one decided the
// outcome, so it is the one reported: "localhost" refused on ::1 and then
// turned down by the server at 127.0.0.1 is a login problem, and reporting
// the first line would send the user to check whether Postgres is running.
func TestQueryErrorMapping(t *testing.T) {
	const refused = "psql: error: connection to server at \"127.0.0.1\", port 1 failed: Connection refused\n" +
		"\tIs the server running on that host and accepting TCP/IP connections?\n"
	// libpq's shape when the server answered and said no.
	const badLogin = "psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: " +
		"FATAL:  password authentication failed for user \"litellm\"\n"
	const noSuchDB = "psql: error: connection to server on socket \"/tmp/.s.PGSQL.5432\" failed: " +
		"FATAL:  database \"litellm\" does not exist\n"
	const noSuchHost = "psql: error: could not translate host name \"db.example\" to address: " +
		"nodename nor servname provided, or not known\n"
	const ipv6 = "psql: error: connection to server at \"::1\", port 5432 failed: Connection refused\n"
	const oddHost = "psql: error: connection to server at \"not a host!\", port 5432 failed: Connection refused\n"
	const oddPort = "psql: error: connection to server at \"db.example\", port 54x failed: Connection refused\n"
	const oddName = "psql: error: could not translate host name \"not a host!\" to address: " +
		"nodename nor servname provided, or not known\n"
	const oddSocket = "psql: error: connection to server on socket \"/tmp/a b/.s.PGSQL.5432\" failed: No such file or directory\n"
	// "localhost" has two addresses: ::1 refuses, the server at 127.0.0.1
	// answers and turns the login down.
	const twoAddresses = "psql: error: connection to server at \"localhost\" (::1), port 5432 failed: Connection refused\n" +
		"\tIs the server running on that host and accepting TCP/IP connections?\n" +
		"connection to server at \"localhost\" (127.0.0.1), port 5432 failed: " +
		"FATAL:  password authentication failed for user \"litellm\"\n"
	// A two-host string: the first name does not resolve, the second refuses.
	const twoHosts = "psql: error: could not translate host name \"db-a.example\" to address: " +
		"nodename nor servname provided, or not known\n" +
		"connection to server at \"db-b.example\" (10.0.0.6), port 6432 failed: Connection refused\n"
	// The last attempt has a host that is not shaped like one: the earlier
	// attempt's address must not be shown beside the later attempt's reason.
	const lastIsOdd = "psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: Connection refused\n" +
		"connection to server at \"not a host!\", port 5432 failed: Operation timed out\n"
	const withheld = "cannot reach the LiteLLM database: psql could not connect; its message is not shown because it can quote the connection string"
	cases := []struct {
		name           string
		stdout, stderr string
		exit           int
		want           error
		wantMsg        string
	}{
		{"connection refused", "", refused, 2, ErrUnreachable,
			"cannot reach the LiteLLM database at 127.0.0.1:1: Connection refused"},
		{"login refused", "", badLogin, 2, ErrUnreachable,
			`cannot reach the LiteLLM database at db.example:5432: password authentication failed for user "..."`},
		{"no such database, over a socket", "", noSuchDB, 2, ErrUnreachable,
			`cannot reach the LiteLLM database at socket /tmp/.s.PGSQL.5432: database "..." does not exist`},
		{"no such host", "", noSuchHost, 2, ErrUnreachable,
			"cannot reach the LiteLLM database at db.example: the database host name did not resolve"},
		{"an IPv6 address", "", ipv6, 2, ErrUnreachable,
			"cannot reach the LiteLLM database at [::1]:5432: Connection refused"},
		{"a host that is not shaped like one", "", oddHost, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a port that is not a number", "", oddPort, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a socket path that is not shaped like one", "", oddSocket, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: No such file or directory"},
		{"an unresolved name that is not shaped like a host", "", oddName, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: the database host name did not resolve"},
		{"two addresses, the second one refused the login", "", twoAddresses, 2, ErrUnreachable,
			`cannot reach the LiteLLM database at localhost:5432: password authentication failed for user "..."`},
		{"two hosts, the last one refused", "", twoHosts, 2, ErrUnreachable,
			"cannot reach the LiteLLM database at db-b.example:6432: Connection refused"},
		{"two attempts, the last with an odd host", "", lastIsOdd, 2, ErrUnreachable,
			"cannot reach the LiteLLM database: Operation timed out"},
		{"an unknown connection failure", "", "psql: error: something libpq has not said before\n", 2, ErrUnreachable, withheld},
		{"exit 2 and no message", "", "", 2, ErrUnreachable, withheld},
		{"sql error", "", "ERROR:  relation \"LiteLLM_SpendLogs\" does not exist\nLINE 1: ...\n", 1, ErrQuery,
			`the spend query failed: ERROR:  relation "LiteLLM_SpendLogs" does not exist`},
		{"a failure that is not the server's", "", "psql: error: out of memory\n", 1, ErrQuery,
			"the spend query failed: psql exited with status 1"},
		{"silent failure", "", "", 3, ErrQuery,
			"the spend query failed: psql exited with status 3"},
		{"not json", "NOTICE: hello\n", "", 0, ErrQuery, ""},
		{"empty output", "", "", 0, ErrQuery, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubPsql(t, c.stdout, c.stderr, c.exit)
			_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.wantMsg != "" && err.Error() != c.wantMsg {
				t.Errorf("err = %q\nwant  %q", err, c.wantMsg)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("err = %q, want one line", err)
			}
		})
	}

	t.Run("no psql", func(t *testing.T) {
		old := lookPath
		lookPath = func(string) (string, error) { return "", errors.New("not found") }
		t.Cleanup(func() { lookPath = old })
		_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
		if !errors.Is(err, ErrNoPsql) || err.Error() != "psql not found on PATH" {
			t.Fatalf("err = %v, want ErrNoPsql", err)
		}
	})
}

// TestQueryRefusesABlankConnectionString verifies an empty or
// whitespace-only connection string is refused with ErrNoConnectionString
// and that psql is neither looked for nor run. libpq reads a string with no
// host as "the default socket on this machine", where a Postgres that is
// not the proxy's may be listening; the callers filter blanks today, and
// this makes the package safe whoever calls it next.
func TestQueryRefusesABlankConnectionString(t *testing.T) {
	call := stubPsql(t, "[]", "", 0)
	looked := 0
	lookPath = func(string) (string, error) { looked++; return "/fake/psql", nil }
	for _, dsn := range []string{"", " ", "\t\n ", "\u00a0\u2003"} {
		_, err := Query(context.Background(), dsn, winStart, winEnd)
		if !errors.Is(err, ErrNoConnectionString) || err.Error() != "no connection string for the LiteLLM database" {
			t.Errorf("Query(%q) err = %v, want ErrNoConnectionString", dsn, err)
		}
	}
	if looked != 0 || call.n != 0 {
		t.Errorf("psql was looked up %d times and run %d times, want neither", looked, call.n)
	}
}

// TestQueryCancelledIsNotATimeout verifies a caller that cancels its
// context gets "cancelled", not "no answer within 10s": nothing was learned
// about the database, and a note blaming it would send the user to debug a
// server that may be fine.
func TestQueryCancelledIsNotATimeout(t *testing.T) {
	stubPsql(t, "", "", -1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Query(ctx, "postgresql://h/db", winStart, winEnd)
	if !errors.Is(err, ErrQuery) || errors.Is(err, ErrUnreachable) || err.Error() != "the spend query failed: cancelled before psql answered" {
		t.Fatalf("err = %v, want ErrQuery saying the query was cancelled", err)
	}
}

// TestQueryNeverLeaksTheConnectionString verifies the only pieces of a
// connection string an error can show are the host and port, and those only
// when the string is written so libpq cannot have taken them from the user
// name or password. Every credential here starts with FAKE: the user, the
// password and the database name must never appear, whatever psql said.
//
// The first block is well-formed strings — a password with percent-escapes,
// a keyword string, a socket directory — where the address is shown and the
// names the server quotes back are blanked. The second is strings libpq
// mis-splits: an unencoded "/" or "@" in the password, a bad percent escape,
// a space in a keyword value. Four of those messages are the ones psql 16
// prints (recorded with made-up credentials against an address nothing
// listens on). The others are written in libpq's connect-failure shape for
// a mis-split string that happens to name a host that answers — there the
// "host" is the user name or the tail of the password, and the "port" a
// piece of the password, so the address is left out. Go's url.Parse
// rejects some of these strings and reads others the way libpq does, so
// nothing that asks Go for "the password" can protect them.
//
// The third block is reasons that name the user or database outside ASCII
// double quotes — a server answering in German (»x«), a name that itself
// holds a double quote, a connection pooler's unquoted wording — and one
// line per address for a string with several. A reason is shown only when
// it is one the package lists; the address still is, since it is checked
// separately.
// `wt stats` prints the error on the terminal, which ends up in scrollback,
// history files and bug reports.
func TestQueryNeverLeaksTheConnectionString(t *testing.T) {
	const withheld = "cannot reach the LiteLLM database: psql could not connect; its message is not shown because it can quote the connection string"
	const refusedAtDB = "psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: Connection refused\n"
	const failedAtDB = "psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: "
	const reasonWithheld = "cannot reach the LiteLLM database at db.example:5432: the reason psql gave is not shown because it can quote the connection string"
	const goodURI = "postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb"
	cases := []struct {
		name, dsn, stderr string
		exit              int
		want              string
	}{
		// Well-formed: the address is shown, nothing else.
		{"a refused login naming the user and database", "postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: FATAL:  no pg_hba.conf entry for host \"10.0.0.9\", user \"FAKEuser\", database \"FAKEdb\", no encryption\n", 2,
			`cannot reach the LiteLLM database at db.example:5432: no pg_hba.conf entry for host "...", user "...", database "...", no encryption`},
		{"a password with percent-escapes", "postgresql://FAKEuser:FAKEp%40ss%2Fw%25@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"db.example\" (10.0.0.5), port 5432 failed: FATAL:  password authentication failed for user \"FAKEuser\"\n", 2,
			`cannot reach the LiteLLM database at db.example:5432: password authentication failed for user "..."`},
		{"a keyword string", "host=db.example port=5432 user=FAKEuser password=FAKEpw dbname=FAKEdb",
			refusedAtDB, 2,
			"cannot reach the LiteLLM database at db.example:5432: Connection refused"},
		{"a socket directory", "postgresql://FAKEuser:FAKEpw@%2Fvar%2Frun%2Fpostgresql/FAKEdb",
			"psql: error: connection to server on socket \"/var/run/postgresql/.s.PGSQL.5432\" failed: FATAL:  database \"FAKEdb\" does not exist\n", 2,
			`cannot reach the LiteLLM database at socket /var/run/postgresql/.s.PGSQL.5432: database "..." does not exist`},
		{"a query failure that echoes the whole string", "postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb",
			"psql: error: could not use postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb\n", 1,
			"the spend query failed: psql exited with status 1"},
		{"a connection failure that echoes the whole string", "postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb",
			"psql: error: could not use postgresql://FAKEuser:FAKEpw@db.example:5432/FAKEdb\n", 2, withheld},

		// Mis-split by libpq: no address, whatever psql called the host.
		{"a slash in the password, read as host:port", "postgresql://FAKEuser:FAKEabc/def@db.example:5432/FAKEdb",
			"psql: error: invalid integer value \"FAKEabc\" for connection option \"port\"\n", 2, withheld},
		{"a slash after digits in the password, read as a real port", "postgresql://FAKEuser:54321/FAKEdef@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"FAKEuser\" (10.0.0.7), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a bad percent escape in the password", "postgresql://FAKEuser:FAKEpa%zzword@db.example:5432/FAKEdb",
			"psql: error: invalid percent-encoded token: \"FAKEpa%zzword\"\n", 2, withheld},
		{"an @ in the password, read as the host", "postgresql://FAKEuser:FAKEp@ss@db.example:5432/FAKEdb",
			"psql: error: could not translate host name \"ss@db.example\" to address: nodename nor servname provided, or not known\n", 2,
			"cannot reach the LiteLLM database: the database host name did not resolve"},
		{"an @ in the password, then a comma, read as a host list", "postgresql://FAKEuser:FAKEp@ss,word@db.example:5432/FAKEdb",
			"psql: error: could not translate host name \"ss\" to address: nodename nor servname provided, or not known\n", 2,
			"cannot reach the LiteLLM database: the database host name did not resolve"},
		{"an @ in the password, then a comma, read as a host list that refuses", "postgresql://FAKEuser:FAKEp@ss,word@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"ss\" (10.0.0.7), port 5432 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"an @ in the password, and a tail that is a host name", "postgresql://FAKEuser:FAKEp@ss.example:54321/x@db.example:5432/FAKEdb",
			"psql: error: connection to server at \"ss.example\" (10.0.0.7), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a keyword string with a space in the password", "host=db.example port=5432 user=FAKEuser password=FAKEabc FAKEdef",
			"psql: error: missing \"=\" after \"FAKEdef\" in connection info string\n", 2, withheld},
		{"a keyword string with a quoted password", "host=db.example port=5432 user=FAKEuser password='FAKE a'",
			refusedAtDB, 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a keyword string whose password has a space before port=", "host=db.example user=FAKEuser password=FAKEab port=54321",
			"psql: error: connection to server at \"db.example\" (10.0.0.5), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a keyword string whose sslpassword has a space before port=", "host=db.example sslpassword=FAKEab port=54321",
			"psql: error: connection to server at \"db.example\" (10.0.0.5), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},
		{"a keyword string that names the host after the database", "dbname=FAKEdb host=ss.example port=54321",
			"psql: error: connection to server at \"ss.example\" (10.0.0.7), port 54321 failed: Connection refused\n", 2,
			"cannot reach the LiteLLM database: Connection refused"},

		// Reasons that name the user or the database where blanking ASCII
		// quotes does not reach.
		{"a server that answers in German", goodURI,
			failedAtDB + "FATAL:  Passwort-Authentifizierung für Benutzer »FAKEuser« fehlgeschlagen\n", 2, reasonWithheld},
		{"a user name that holds a double quote", "postgresql://FAKE%22user:FAKEpw@db.example:5432/FAKEdb",
			failedAtDB + "FATAL:  password authentication failed for user \"FAKE\"user\"\n", 2, reasonWithheld},
		{"a pooler that names the user without quotes", goodURI,
			failedAtDB + "FATAL:  no such user: FAKEuser\n", 2, reasonWithheld},
		{"a pooler that names the database in single quotes", goodURI,
			failedAtDB + "FATAL:  no such database 'FAKEdb'\n", 2, reasonWithheld},
		{"a known reason with a tail", goodURI,
			failedAtDB + "Connection refused by FAKEuser\n", 2, reasonWithheld},
		{"two addresses, the last reason unquoted", "postgresql://FAKEuser:FAKEpw@localhost:5432/FAKEdb",
			"psql: error: connection to server at \"localhost\" (::1), port 5432 failed: Connection refused\n" +
				"connection to server at \"localhost\" (127.0.0.1), port 5432 failed: FATAL:  no such user: FAKEuser\n", 2,
			"cannot reach the LiteLLM database at localhost:5432: the reason psql gave is not shown because it can quote the connection string"},
		{"two addresses, the last line not in a known shape", "postgresql://FAKEuser:FAKEpw@localhost:5432/FAKEdb",
			"psql: error: connection to server at \"localhost\" (::1), port 5432 failed: Connection refused\n" +
				"FATAL:  no such user: FAKEuser in postgresql://FAKEuser:FAKEpw@localhost:5432/FAKEdb\n", 2,
			"cannot reach the LiteLLM database at localhost:5432: Connection refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubPsql(t, "", c.stderr, c.exit)
			_, err := Query(context.Background(), c.dsn, winStart, winEnd)
			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			if err.Error() != c.want {
				t.Errorf("err = %q\nwant  %q", err, c.want)
			}
			for _, piece := range []string{"FAKE", "zzword", "ss@", "ss:", "at ss", "ss.example", "54321", "10.0.0", "%", "postgresql://", "»", "'"} {
				if strings.Contains(err.Error(), piece) {
					t.Errorf("err = %q leaks %q from the connection string", err, piece)
				}
			}
		})
	}
}

// TestQueryRunsARealProcess runs Query end to end against a shell script
// standing in for psql: the arguments and the two environment variables
// reach a real child process, and its stdout comes back parsed. The stubbed
// tests cannot catch a mistake in the exec plumbing itself (a dropped
// environment, stdout and stderr swapped).
func TestQueryRunsARealProcess(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "argv")
	fakePsqlBinary(t, `printf '%s\n' "$@" > "`+rec+`"
printf 'timeout=%s tz=%s\n' "$PGCONNECT_TIMEOUT" "$PGTZ" >> "`+rec+`"
echo 'noise on stderr' >&2
echo '[{"model":"m","requests":2,"prompt_tokens":10,"completion_tokens":20,"spend":0.5}]'
`)
	got, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Rows) != 1 || got.Rows[0] != (Row{Model: "m", Requests: 2, PromptTokens: 10, CompletionTokens: 20, Spend: 0.5}) {
		t.Errorf("Rows = %+v, want the script's one row", got.Rows)
	}
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("the script did not record its arguments: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-d", "postgresql://h/db", "-c", querySQL(winStart, winEnd), "timeout=3 tz=UTC"}
	if !slices.Equal(lines, want) {
		t.Errorf("the child saw\n%q\nwant\n%q", lines, want)
	}
}

// TestQueryGivesUpAtTheDeadline verifies a psql that never answers is
// killed at the deadline and reported as unreachable. `wt stats` is a
// report people run while waiting for something else; a database host that
// accepts the connection and then hangs must cost seconds, not the
// terminal.
func TestQueryGivesUpAtTheDeadline(t *testing.T) {
	fakePsqlBinary(t, "exec sleep 30\n")
	old := deadline
	deadline = 200 * time.Millisecond
	t.Cleanup(func() { deadline = old })

	began := time.Now()
	_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "no answer within 200ms") {
		t.Fatalf("err = %v, want ErrUnreachable naming the deadline", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Query took %s, want it to return at the deadline", took)
	}
}

// TestQueryExitStatusFromARealProcess verifies a real child's exit status 2
// and stderr are read as "unreachable" — the status psql uses for a failed
// connection. A runner that reported every non-zero exit as a Go error
// would turn a database that is simply down into "the spend query failed".
func TestQueryExitStatusFromARealProcess(t *testing.T) {
	fakePsqlBinary(t, `echo 'psql: error: connection to server at "127.0.0.1", port 1 failed: Connection refused' >&2
exit 2
`)
	_, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd)
	if !errors.Is(err, ErrUnreachable) || err.Error() != "cannot reach the LiteLLM database at 127.0.0.1:1: Connection refused" {
		t.Fatalf("err = %v, want ErrUnreachable with the address and libpq's reason", err)
	}
}

// TestSeamsFailClosedByDefault verifies this package's TestMain: with
// neither seam stubbed, Query cannot find psql, so a test added later that
// forgets to stub still cannot run the real binary against the developer's
// database.
func TestSeamsFailClosedByDefault(t *testing.T) {
	if _, err := Query(context.Background(), "postgresql://h/db", winStart, winEnd); !errors.Is(err, ErrNoPsql) {
		t.Fatalf("err = %v, want ErrNoPsql from the TestMain default", err)
	}
	if _, _, _, err := runPsql(context.Background(), "psql", nil, nil); err == nil {
		t.Fatal("the default runPsql ran something; want a hard failure")
	}
}
