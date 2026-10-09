package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// procInfo is what the process table says about one live pid: the evidence wt
// needs before it signals a process it did not start in this run.
type procInfo struct {
	uid    int
	pgid   int
	zombie bool
	// exiting: the process is in the table and has no arguments to give —
	// the kernel is taking it apart after a fatal signal, which for a server
	// holding tens of gigabytes takes a noticeable part of a second, before it
	// becomes a zombie or disappears. argv is then nil, so such a process is
	// never identified as anything and never signalled.
	exiting bool
	// start is the process's start time, in a form that is only ever compared
	// for equality. It survives exec, so it is the same before and after
	// `mtplx serve` replaces itself with its daemon.
	start string
	// argv is the exact argument vector, one element per argument — not a
	// command line split on spaces.
	argv []string
}

// The two process seams: reading the process table and sending a signal.
// defaultEnv hands them to the engine; IsolateProcessesForTest closes both, so
// a test binary reaches a real process only where a test says so.
var (
	// describeProc reports pid's entry in the process table; found is false
	// when no such process exists.
	describeProc = realDescribeProc
	// signalProc sends sig to pid, or to the process group -pid when pid is
	// negative (kill(2)).
	signalProc = realSignalProc
)

func realSignalProc(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// mtplxPidfileEnv names another path for the mtplx pidfile; the log and wt's
// start record follow it. It exists so a test, or a built wt run by hand
// against scratch state, never touches the real pidfile. llmbench does not
// read it: the two tools share a pidfile only at the default path.
const mtplxPidfileEnv = "WT_MTPLX_PIDFILE"

// mtplxProcess is the pidfile and log of the mtplx server: the paths llmbench's
// mtplx backend uses too, unless WT_MTPLX_PIDFILE names another pidfile.
func mtplxProcess() pidProcess {
	p := pidProcess{name: "mtplx", pidfile: "/tmp/local-ai-setup-mtplx.pid", logfile: "/tmp/local-ai-setup-mtplx.log"}
	if path := os.Getenv(mtplxPidfileEnv); path != "" {
		p.pidfile = path
		p.logfile = strings.TrimSuffix(path, ".pid") + ".log"
	}
	return p
}

// IsolateProcessesForTest is for a package's TestMain: it makes the
// process-table read and the signal fail for the rest of the process, and
// points the mtplx pidfile at a directory that does not exist. A test that
// reaches the production stop path without stubbing it then reads no real
// pidfile and signals no real process. Production code never calls it — like
// config.IsolateConfigHomeForTest, it exists so cross-package tests share one
// definition.
func IsolateProcessesForTest() {
	describeProc = func(pid int) (procInfo, bool, error) {
		return procInfo{}, false, fmt.Errorf("describeProc not stubbed in this test (pid %d)", pid)
	}
	signalProc = func(pid int, sig syscall.Signal) error {
		return fmt.Errorf("signalProc not stubbed in this test (pid %d, signal %d)", pid, sig)
	}
	_ = os.Setenv(mtplxPidfileEnv, filepath.Join(os.TempDir(), "wt-test-no-mtplx-"+strconv.Itoa(os.Getpid()), "mtplx.pid"))
}

// isMtplxServer reports whether argv is the mtplx server for port, in either
// of the two forms the pid wt spawned takes (mtplx 2.12.0):
//
//   - the wrapper: an interpreter (sh or python — mtplx is a script) running
//     a file named mtplx with the `serve` subcommand, exactly what wt passes
//     (argv[1] and argv[2]); or mtplx itself as argv[0] with `serve` next;
//   - the daemon `mtplx serve` then execs in the same pid, which is the one
//     that loads the weights: python with `-m mtplx.server.openai` after
//     nothing but interpreter options.
//
// The interpreter's name is compared without regard to case: a macOS
// framework Python re-execs into `.../Python.app/Contents/MacOS/Python`.
//
// Either must be followed by `--port` and this port as two separate
// arguments. Positions and whole arguments are compared, never substrings, so
// a command that only mentions the words — `vim mtplx serve --port 8003` — is
// not a server.
func isMtplxServer(argv []string, port int) bool {
	rest := -1
	if len(argv) >= 2 && filepath.Base(argv[0]) == "mtplx" && argv[1] == "serve" {
		rest = 2
	} else if len(argv) >= 3 {
		interp := filepath.Base(argv[0])
		python := strings.HasPrefix(strings.ToLower(interp), "python")
		if (python || interp == "sh" || interp == "bash") && filepath.Base(argv[1]) == "mtplx" && argv[2] == "serve" {
			rest = 3
		} else if python {
			for i := 1; i+1 < len(argv) && strings.HasPrefix(argv[i], "-"); i++ {
				if argv[i] == "-m" {
					if argv[i+1] == "mtplx.server.openai" {
						rest = i + 2
					}
					break
				}
			}
		}
	}
	if rest < 0 {
		return false
	}
	want := strconv.Itoa(port)
	for i := rest; i+1 < len(argv); i++ {
		if argv[i] == "--port" && argv[i+1] == want {
			return true
		}
	}
	return false
}
