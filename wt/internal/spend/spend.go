// Package spend reads per-model request, token and cost totals from the
// LiteLLM proxy's Postgres spend log ("LiteLLM_SpendLogs") by running one
// aggregated query through psql. wt links no Postgres driver: psql is on
// every machine that runs the proxy's database, and its absence is one of
// the cases Query reports rather than a build dependency.
//
// The connection string never reaches psql's command line, where `ps`
// would show the database password to every user of the machine: conn.go
// turns it into libpq's environment variables, and says why that way.
package spend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNoPsql: there is no psql on PATH.
	ErrNoPsql = errors.New("psql not found on PATH")
	// ErrUnreachable: psql could not connect (its exit status 2), or gave no
	// answer before the deadline.
	ErrUnreachable = errors.New("cannot reach the LiteLLM database")
	// ErrQuery: psql connected and the query failed, or its output was not
	// the JSON document the query asks for.
	ErrQuery = errors.New("the spend query failed")
	// ErrNoConnectionString: Query was given an empty or whitespace-only
	// connection string. It is the "not configured" answer, and psql is not
	// run: libpq reads a string with no host as "the local default socket",
	// where a server that is not the proxy's database may be listening.
	ErrNoConnectionString = errors.New("no connection string for the LiteLLM database")
)

// Row is one model's totals in the window.
type Row struct {
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Spend            float64 `json:"spend"`
}

// Result is the answer to one Query. Rows never holds a row with an empty
// Model: requests the proxy logged without a model group are counted in
// Unattributed instead. Rows comes in the order the database returned it —
// its collation, which is not Go's byte order — so a caller that needs an
// order sorts.
type Result struct {
	Rows         []Row
	Unattributed int64
}

// connectTimeout is PGCONNECT_TIMEOUT, in seconds: how long psql waits for
// a host that does not answer at all. A refused connection fails at once.
// A connect_timeout in the connection string takes its place.
const connectTimeout = "3"

// deadline bounds the whole psql run. A var so a test can shorten it.
var deadline = 10 * time.Second

// lookPath finds psql. A seam: tests point it at a fake or fail it.
var lookPath = realLookPath

// realLookPath is the production lookPath.
func realLookPath(name string) (string, error) { return exec.LookPath(name) }

// runPsql runs the psql binary with the given arguments and environment and
// returns what it printed and its exit status. A seam: no test may run the
// real psql against a real database, and a test sees through it exactly
// what psql would be given — its arguments and its whole environment.
var runPsql = realRunPsql

// timestampLayout is a Postgres timestamp literal with no zone.
const timestampLayout = "2006-01-02 15:04:05.000000"

// querySQL is the one statement Query runs. It returns a single JSON
// document — an array with one object per model group — so the caller
// parses one value instead of delimited rows whose model ids may contain
// any character.
//
// The two timestamps are the only values interpolated, and Go formats both
// from a time.Time, so nothing a user typed reaches the SQL. They are
// written in UTC with no zone because "startTime" is a zone-less timestamp
// column that LiteLLM fills with UTC.
func querySQL(start, end time.Time) string {
	return `SELECT coalesce(json_agg(t), '[]'::json) FROM (` +
		`SELECT coalesce(model_group, '') AS model, ` +
		`count(*) AS requests, ` +
		`coalesce(sum(prompt_tokens), 0) AS prompt_tokens, ` +
		`coalesce(sum(completion_tokens), 0) AS completion_tokens, ` +
		`coalesce(sum(spend), 0) AS spend ` +
		`FROM "LiteLLM_SpendLogs" ` +
		`WHERE "startTime" >= timestamp '` + start.UTC().Format(timestampLayout) + `' ` +
		`AND "startTime" <= timestamp '` + end.UTC().Format(timestampLayout) + `' ` +
		`GROUP BY 1 ORDER BY 1) t`
}

// Query returns per-model totals for requests the proxy logged between
// start and end, both inclusive. dsn is a libpq connection string or URI.
// psql receives it as environment variables and never as an argument, and
// inherits no PG* variable from wt's own environment (see conn.go).
//
// A blank dsn is refused with ErrNoConnectionString before anything else is
// looked at, so no caller can have psql connect to libpq's default server.
// A dsn that cannot be carried over to the environment faithfully is
// refused with ErrConnectionString, also before psql is looked for.
// Every other failure is one of ErrNoPsql, ErrUnreachable or ErrQuery,
// wrapped with a one-line reason. An ErrUnreachable names the host and port
// psql tried (or the socket), so a wrong address can be seen; no other part
// of dsn appears in any error — not the user, the password or the database
// name. psql's own text is used only in the shapes unreachable and
// queryReason allow.
func Query(ctx context.Context, dsn string, start, end time.Time) (Result, error) {
	if strings.TrimSpace(dsn) == "" {
		return Result{}, ErrNoConnectionString
	}
	conn, err := connEnv(dsn)
	if err != nil {
		return Result{}, err
	}
	bin, err := lookPath("psql")
	if err != nil {
		return Result{}, ErrNoPsql
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()

	// No -d: an argument is readable by every user of the machine.
	args := []string{"-X", "-w", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-c", querySQL(start, end)}
	stdout, stderr, exit, err := runPsql(ctx, bin, args, psqlEnv(os.Environ(), conn))
	switch {
	case err == nil && exit == 0:
		// psql answered: its output is parsed below, even when the deadline
		// or the caller's context fired in the same instant. A run that
		// completed is an answer; testing the clock first would report "no
		// answer within …" over the rows psql just printed.
	case parent.Err() != nil:
		// The caller's own context ended: not our deadline, and nothing is
		// known about the database.
		return Result{}, fmt.Errorf("%w: cancelled before psql answered", ErrQuery)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return Result{}, fmt.Errorf("%w: no answer within %s", ErrUnreachable, deadline)
	case err != nil:
		// The process could not be run at all. Go's error names the binary,
		// never its arguments.
		return Result{}, fmt.Errorf("%w: psql could not be run (%v)", ErrQuery, err)
	case exit == 2:
		return Result{}, unreachable(dsn, stderr)
	case exit != 0:
		return Result{}, fmt.Errorf("%w: %s", ErrQuery, queryReason(stderr, exit))
	}

	var rows []Row
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &rows); err != nil {
		return Result{}, fmt.Errorf("%w: psql did not print the JSON document asked for (%v)", ErrQuery, err)
	}
	res := Result{Rows: make([]Row, 0, len(rows))}
	for _, r := range rows {
		if r.Model == "" {
			res.Unattributed += r.Requests
			continue
		}
		res.Rows = append(res.Rows, r)
	}
	return res, nil
}

// realRunPsql runs bin and reports a non-zero exit as a status, not an
// error: err is set only when the process could not be run at all.
func realRunPsql(ctx context.Context, bin string, args, env []string) (stdout, stderr []byte, exit int, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	// A killed psql can leave a child holding the pipes; do not wait on it.
	cmd.WaitDelay = time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// connectFailed matches the line libpq prints when it reached for a server
// and got no session, in its TCP and its socket form, capturing the host,
// the port, the socket path and the reason:
//
//	connection to server at "db" (10.0.0.5), port 5432 failed: <reason>
//	connection to server on socket "/tmp/.s.PGSQL.5432" failed: <reason>
var connectFailed = regexp.MustCompile(`^connection to server (?:at "([^"]*)"(?: \([^)]*\))?, port (\S+)|on socket "([^"]*)") failed: (.+)$`)

// translateFailed matches libpq's line for a host name with no address,
// capturing the name.
var translateFailed = regexp.MustCompile(`^could not translate host name "([^"]*)" to address: `)

// The only shapes of host, port and socket path an error may show: a host
// name or an IPv4 or IPv6 address, a port number, and a Postgres socket
// file. Anything else psql printed in their place is left out.
var (
	hostShape   = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	portShape   = regexp.MustCompile(`^[0-9]{1,5}$`)
	socketShape = regexp.MustCompile(`^/[A-Za-z0-9._/-]*\.s\.PGSQL\.[0-9]{1,5}$`)
)

// quotedValue matches a double-quoted value in a libpq or server message.
var quotedValue = regexp.MustCompile(`"[^"]*"`)

// knownReason is every reason for a failed connection that an error may
// show, matched against the whole reason after its double-quoted values
// were blanked to "...". The first group is the operating system's and
// libpq's own; the second is the server's refusals (its FATAL lines), which
// name the user and the database only inside the quotes just blanked.
//
// It is an allow-list because blanking alone is not enough: a server that
// answers in another language quotes names its own way (»x«), a name that
// holds a double quote leaves its tail outside the blank, and a connection
// pooler in front of Postgres words its refusals as it likes. A reason that
// is not on the list is replaced by unknownReason.
var knownReason = regexp.MustCompile(`^(?:` + strings.Join([]string{
	`Connection refused`,
	`Connection timed out`,
	`Operation timed out`,
	`timeout expired`,
	`No route to host`,
	`Network is unreachable`,
	`Host is down`,
	`Connection reset by peer`,
	`No such file or directory`,
	`Permission denied`,
	`server closed the connection unexpectedly`,

	`password authentication failed for user "\.\.\."`,
	`(?:Peer|Ident) authentication failed for user "\.\.\."`,
	`role "\.\.\." does not exist`,
	`role "\.\.\." is not permitted to log in`,
	`database "\.\.\." does not exist`,
	`database "\.\.\." is not currently accepting connections`,
	`permission denied for database "\.\.\."`,
	`(?:no pg_hba\.conf entry|pg_hba\.conf rejects connection) for host "\.\.\.", user "\.\.\.", database "\.\.\.", (?:no encryption|SSL encryption|GSS encryption|SSL on|SSL off)`,
	`too many connections for (?:role|database) "\.\.\."`,
	`sorry, too many clients already`,
	`the database system is (?:starting up|shutting down|in recovery mode)`,
}, "|") + `)$`)

// unknownReason stands in for a reason knownReason does not list.
const unknownReason = "the reason psql gave is not shown because it can quote the connection string"

// stderrLines are the non-empty lines psql wrote, without its
// "psql: error: " prefix.
func stderrLines(stderr []byte) []string {
	var out []string
	for _, line := range strings.Split(string(stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, strings.TrimPrefix(line, "psql: error: "))
		}
	}
	return out
}

// unreachable turns psql's stderr for exit status 2 into an ErrUnreachable
// that says where psql tried to connect and why it failed, and nothing else
// from the connection string:
//
//	cannot reach the LiteLLM database at 127.0.0.1:5432: Connection refused
//
// psql quotes pieces of a connection string it cannot use, and the piece
// can be the password: an unencoded "/" in one makes libpq read
// user:password as host:port and report `invalid integer value "<password>"
// for connection option "port"`. So nothing is passed through as it came.
// Of a "connection to server … failed: <reason>" line the reason is kept
// with every quoted value blanked — the server's refusals name the user and
// the database (`password authentication failed for user "x"`) — and only
// when what is left is a reason knownReason lists; the host and port, or
// the socket path, are kept only as address allows. A host name that did
// not resolve is named on the same terms. Anything else is withheld.
//
// libpq prints one such line for each address it tried: a name with two
// addresses ("localhost" is ::1 and 127.0.0.1), or a string that lists
// several hosts. The last line is the attempt that decided the outcome — a
// refused ::1 followed by a login the server at 127.0.0.1 turned down is a
// login problem — so the last line libpq printed in a known shape is the
// one reported. Every line goes through the same shapes, whichever is
// picked.
func unreachable(dsn string, stderr []byte) error {
	plain := plainAddress(dsn)
	where, reason := "", "psql could not connect; its message is not shown because it can quote the connection string"
	for _, line := range stderrLines(stderr) {
		if m := connectFailed.FindStringSubmatch(line); m != nil {
			reason = quotedValue.ReplaceAllString(strings.TrimSpace(strings.TrimPrefix(m[4], "FATAL:")), `"..."`)
			if !knownReason.MatchString(reason) {
				reason = unknownReason
			}
			where = ""
			if plain {
				where = address(m[1], m[2], m[3])
			}
			continue
		}
		if m := translateFailed.FindStringSubmatch(line); m != nil {
			reason, where = "the database host name did not resolve", ""
			if plain && hostShape.MatchString(m[1]) {
				where = m[1]
			}
		}
	}
	if where == "" {
		return fmt.Errorf("%w: %s", ErrUnreachable, reason)
	}
	return fmt.Errorf("%w at %s: %s", ErrUnreachable, where, reason)
}

// address formats where libpq said it tried to connect: host:port, or
// "socket <path>". It returns "" when a piece is not in the shape of a host
// name, an IP address, a port number or a Postgres socket file.
func address(host, port, socket string) string {
	switch {
	case socket != "":
		if socketShape.MatchString(socket) {
			return "socket " + socket
		}
	case hostShape.MatchString(host) && portShape.MatchString(port):
		return net.JoinHostPort(host, port)
	}
	return ""
}

// plainAddress reports whether dsn is written so that the host and port
// libpq reports are the ones the user meant, and not pieces of the user name
// or password.
//
// libpq splits a URI at the first "/" or "@" it meets, so a password with
// an unencoded "/" turns user:password into host:port — `u:1234/x@db` is
// host "u", port 1234. That leaves an "@" after the authority, where a
// well-formed URI has none. An unencoded "@" in a password puts its tail in
// the host; hostShape refuses most tails, but not one that a "," makes look
// like a host list (`p@ss,word@db` reads hosts "ss" and "word@db"), so a
// URI with more than one "@" in its authority is refused too.
//
// In a keyword string, a value with an unquoted space ends early, and what
// follows it can be read as a host or a port. Any value can be a secret —
// password, sslpassword, passfile, a service name — so the address counts
// only when host, hostaddr and port are written before every other
// keyword, and no field is anything but a plain key=value.
//
// For any other string the address is left out of the error; the reason
// alone is still shown.
func plainAddress(dsn string) bool {
	rest, isURI := strings.CutPrefix(dsn, "postgresql://")
	if !isURI {
		rest, isURI = strings.CutPrefix(dsn, "postgres://")
	}
	if isURI {
		i := strings.IndexAny(rest, "/?#")
		authority := rest
		if i >= 0 {
			authority = rest[:i]
		}
		return strings.Count(authority, "@") <= 1 && (i < 0 || !strings.Contains(rest[i:], "@"))
	}
	fields := strings.Fields(dsn)
	afterOther := false
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		isAddress := k == "host" || k == "hostaddr" || k == "port"
		switch {
		case !ok || k == "" || strings.ContainsAny(v, `'\`):
			return false
		case isAddress && afterOther:
			return false
		case !isAddress:
			afterOther = true
		}
	}
	return len(fields) > 0
}

// queryReason is the reason for a failure after psql connected: the
// server's own ERROR line about the statement (`relation
// "LiteLLM_SpendLogs" does not exist`), which is about the SQL and cannot
// contain the connection string. Any other text is replaced by the exit
// status.
func queryReason(stderr []byte, exit int) string {
	if lines := stderrLines(stderr); len(lines) > 0 && strings.HasPrefix(lines[0], "ERROR:") {
		return lines[0]
	}
	return fmt.Sprintf("psql exited with status %d", exit)
}
