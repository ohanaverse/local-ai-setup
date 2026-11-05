package removal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lsofRecord is one process's cwd as `lsof -F0` prints it: NUL after each
// field, a newline closing each set (lsof always adds the f field).
func lsofRecord(pid, ppid int, cmd, cwd string) string {
	return "p" + strconv.Itoa(pid) + "\x00R" + strconv.Itoa(ppid) + "\x00c" + cmd + "\x00\nfcwd\x00n" + cwd + "\x00\n"
}

// cannedLsof is a finder whose lsof answers out (and records its arguments);
// it never runs the real lsof. self is the process the finder runs in.
func cannedLsof(out string, args *[]string) lsofFinder {
	return lsofFinder{
		run: func(_ context.Context, a ...string) ([]byte, error) {
			if args != nil {
				*args = a
			}
			return []byte(out), nil
		},
		timeout: time.Second,
		self:    999999,
	}
}

// TestLsofFinderParsesCwdEntries feeds a canned `lsof -F0` listing: a process
// whose cwd is the worktree itself, one in a subdirectory, one outside, a
// sibling sharing the name prefix (/a/b vs /a/bc) and command names with a
// space and a newline. Only the two inside match, by a separator-safe prefix
// (a substring match would warn about the sibling's process), and the names
// come back as one printable line each.
func TestLsofFinderParsesCwdEntries(t *testing.T) {
	dir := t.TempDir()
	dir = mustResolve(t, dir)
	out := lsofRecord(101, 1, "Google Chrome Helper", dir) +
		lsofRecord(102, 1, "evil\nname", dir+"/sub/deeper") +
		lsofRecord(103, 1, "outside", "/") +
		lsofRecord(104, 1, "sibling", dir+"c") +
		lsofRecord(105, 1, "parent", dir[:strings.LastIndex(dir, "/")])
	var args []string
	got, err := cannedLsof(out, &args).InDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Proc{{101, "Google Chrome Helper"}, {102, "evil name"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("procs %+v, want %+v", got, want)
	}
	for _, a := range []string{"-a", "-d", "cwd", "-n", "-P", "-F0pcRn"} {
		if !containsArg(args, a) {
			t.Errorf("lsof args %q lack %q", args, a)
		}
	}
}

// mustResolve is p with symlinks resolved (macOS /var is /private/var; lsof
// reports the resolved path).
func mustResolve(t *testing.T, p string) string {
	t.Helper()
	r, err := evalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestLsofFinderMatchesAnotherSpellingOfTheDir asks about the worktree by an
// unresolved spelling while lsof reports resolved cwds (macOS /tmp and /var
// are symlinks): the comparison is on resolved paths, or every process in the
// worktree would be missed.
func TestLsofFinderMatchesAnotherSpellingOfTheDir(t *testing.T) {
	dir := t.TempDir()
	resolved := mustResolve(t, dir)
	if resolved == dir {
		t.Skipf("%s has no second spelling here", dir)
	}
	got, err := cannedLsof(lsofRecord(7, 1, "vim", resolved+"/x"), nil).InDir(dir)
	if err != nil || len(got) != 1 || got[0].PID != 7 {
		t.Fatalf("procs %+v, err %v; want pid 7", got, err)
	}
}

// TestLsofFinderDeduplicatesPids lists the same pid twice (lsof can repeat a
// process): it is one process, warned about once.
func TestLsofFinderDeduplicatesPids(t *testing.T) {
	dir := mustResolve(t, t.TempDir())
	out := lsofRecord(5, 1, "zsh", dir) + lsofRecord(5, 1, "zsh", dir+"/a")
	got, err := cannedLsof(out, nil).InDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Proc{{5, "zsh"}}) {
		t.Errorf("procs %+v, want one zsh", got)
	}
}

// TestLsofFinderExcludesItselfAndItsParents puts wt, its parent shell and
// that shell's parent in the worktree, the lsof wt started, plus an unrelated
// editor: wt running from inside the directory is not a reason to warn, so
// its own process, its parent chain and its lsof are left out; the editor is
// not.
func TestLsofFinderExcludesItselfAndItsParents(t *testing.T) {
	dir := mustResolve(t, t.TempDir())
	out := lsofRecord(301, 300, "lsof", dir) +
		lsofRecord(300, 200, "wt", dir) +
		lsofRecord(200, 100, "zsh", dir) +
		lsofRecord(100, 1, "tmux", dir) +
		lsofRecord(400, 1, "vim", dir)
	f := cannedLsof(out, nil)
	f.self = 300
	got, err := f.InDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Proc{{400, "vim"}}) {
		t.Errorf("procs %+v, want only vim", got)
	}
}

// TestLsofFinderTimesOut gives the finder an lsof that never answers (it
// ignores its context): the finder still returns an error within about its
// timeout. An unbounded lsof (a hung network mount) would hold the removal
// forever.
func TestLsofFinderTimesOut(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	f := lsofFinder{
		run:     func(context.Context, ...string) ([]byte, error) { <-block; return nil, nil },
		timeout: 50 * time.Millisecond,
		self:    1,
	}
	start := time.Now()
	_, err := f.InDir(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err %v, want a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %s, want about the 50ms timeout", d)
	}
}

// TestLsofFinderReportsMissingLsof makes lsof absent (and failing): the
// finder returns the error, which the engine turns into a warning; it never
// reads an error as "no processes".
func TestLsofFinderReportsMissingLsof(t *testing.T) {
	for name, runErr := range map[string]error{
		"not installed": &exec.Error{Name: "lsof", Err: exec.ErrNotFound},
		"failed":        errors.New("exit status 2"),
	} {
		t.Run(name, func(t *testing.T) {
			f := lsofFinder{
				run:     func(context.Context, ...string) ([]byte, error) { return nil, runErr },
				timeout: time.Second,
				self:    1,
			}
			got, err := f.InDir(t.TempDir())
			if err == nil || got != nil {
				t.Fatalf("procs %+v, err %v; want an error", got, err)
			}
		})
	}
}

// TestLsofFinderRejectsMalformedOutput hands the finder output with a pid
// that is not a number: a listing it cannot read is an error, never a
// partial "no processes".
func TestLsofFinderRejectsMalformedOutput(t *testing.T) {
	dir := mustResolve(t, t.TempDir())
	_, err := cannedLsof("pabc\x00ccmd\x00\nfcwd\x00n"+dir+"\x00\n", nil).InDir(dir)
	if err == nil {
		t.Fatal("no error for a malformed pid")
	}
}

// TestLsofFinderIsTheDefault checks LsofFinder is bounded by the 5 s
// timeout and excludes this process: the engine relies on the finder to
// bound itself (it sets no timeout of its own).
func TestLsofFinderIsTheDefault(t *testing.T) {
	f, ok := LsofFinder().(lsofFinder)
	if !ok {
		t.Fatalf("LsofFinder is %T", LsofFinder())
	}
	if f.timeout != 5*time.Second || f.self != os.Getpid() || f.run == nil {
		t.Errorf("finder %+v, want a 5s timeout and this pid", f)
	}
}
