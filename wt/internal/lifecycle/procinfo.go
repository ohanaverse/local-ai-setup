package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
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
	// never identified as anything and never signalled. It is inferred from
	// kern.procargs2's errno (EINVAL, or EIO confirmed against kern.proc.pid),
	// not read from a field the kernel sets for the purpose.
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
	// readyStdout runs a readiness command and returns its stdout. defaultEnv
	// hands it to the engine as env.runOut; IsolateProcessesForTest closes it,
	// so a test that reaches a real stop-state read never runs `rec status`.
	readyStdout = runCommandStdout
)

func realSignalProc(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// serverProcess is the pidfile and log of a process family's server: the
// spec's defaults — for mtplx the paths llmbench's mtplx backend uses too —
// unless the spec's PidfileEnv (mtplx: WT_MTPLX_PIDFILE) names another
// pidfile, which the log and wt's start record then follow. The variable
// exists so a test, or a built wt run by hand against scratch state, never
// touches the real pidfile. llmbench does not read it: the two tools share a
// pidfile only at the default path.
func serverProcess(spec provider.ProcessSpec) pidProcess {
	p := pidProcess{name: spec.Binary, pidfile: spec.PidfileDefault, logfile: spec.LogDefault}
	if spec.PidfileEnv == "" {
		return p
	}
	if path := os.Getenv(spec.PidfileEnv); path != "" {
		p.pidfile = path
		p.logfile = strings.TrimSuffix(path, ".pid") + ".log"
	}
	return p
}

// serverProcesses is serverProcess for every process family, keyed by family.
func serverProcesses() map[string]pidProcess {
	procs := map[string]pidProcess{}
	for _, family := range provider.ProcessFamilies() {
		if spec, ok := processSpecFor(family); ok {
			procs[family] = serverProcess(spec)
		}
	}
	return procs
}

// IsolateProcessesForTest is for a package's TestMain: it makes the
// process-table read, the signal and the readiness command fail for the rest of the process, and
// points every process family's pidfile (its spec's PidfileEnv) at a
// directory that does not exist. A test that reaches the production stop path
// without stubbing it then reads no real pidfile and signals no real process.
// Production code never calls it — like config.IsolateConfigHomeForTest, it
// exists so cross-package tests share one definition.
func IsolateProcessesForTest() {
	describeProc = func(pid int) (procInfo, bool, error) {
		return procInfo{}, false, fmt.Errorf("describeProc not stubbed in this test (pid %d)", pid)
	}
	signalProc = func(pid int, sig syscall.Signal) error {
		return fmt.Errorf("signalProc not stubbed in this test (pid %d, signal %d)", pid, sig)
	}
	readyStdout = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("readiness command not stubbed in this test (%s)", name)
	}
	for _, family := range provider.ProcessFamilies() {
		if spec, ok := processSpecFor(family); ok && spec.PidfileEnv != "" {
			_ = os.Setenv(spec.PidfileEnv, filepath.Join(os.TempDir(), "wt-test-no-"+family+"-"+strconv.Itoa(os.Getpid()), family+".pid"))
		}
	}
}

// serverArgvMatches reports whether argv is spec's server for port, in either
// of the two forms the pid wt spawned takes (mtplx 2.12.0 as the example):
//
//   - the wrapper: an interpreter (sh or python — mtplx is a script) running
//     a file named spec.Binary with spec.Subcommand, exactly what wt passes
//     (argv[1] and argv[2]); or spec.Binary itself as argv[0] with
//     spec.Subcommand next;
//   - the daemon the wrapper then execs in the same pid, which is the one
//     that loads the weights: python with `-m spec.DaemonModule` after
//     nothing but interpreter options.
//
// The interpreter's name is compared without regard to case: a macOS
// framework Python re-execs into `.../Python.app/Contents/MacOS/Python`.
//
// Either must be followed by spec.PortFlag and this port as two separate
// arguments. Positions and whole arguments are compared, never substrings, so
// a command that only mentions the words — `vim mtplx serve --port 8003` — is
// not a server.
func serverArgvMatches(spec provider.ProcessSpec, argv []string, port int) bool {
	rest := -1
	if len(argv) >= 2 && filepath.Base(argv[0]) == spec.Binary && argv[1] == spec.Subcommand {
		rest = 2
	} else if len(argv) >= 3 {
		interp := filepath.Base(argv[0])
		python := strings.HasPrefix(strings.ToLower(interp), "python")
		if (python || interp == "sh" || interp == "bash") && filepath.Base(argv[1]) == spec.Binary && argv[2] == spec.Subcommand {
			rest = 3
		} else if python && spec.DaemonModule != "" {
			for i := 1; i+1 < len(argv) && strings.HasPrefix(argv[i], "-"); i++ {
				if argv[i] == "-m" {
					if argv[i+1] == spec.DaemonModule {
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
		if argv[i] == spec.PortFlag && argv[i+1] == want {
			return true
		}
	}
	return false
}
