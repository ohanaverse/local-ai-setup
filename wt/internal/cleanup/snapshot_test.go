package cleanup

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// snapshotDir returns every file under dir with its bytes and mode, so a
// test can tell whether anything changed.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += ":" + string(b)
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestGatherReadsClaimsReadOnly writes a live claim on the root (a child
// `sleep` this test owns, with its real start time) beside a stale one, then
// gathers. Why: another live wt session in the root must make the root row in
// use (held), and cleanup only reads claims: it must not write, prune the
// stale claim, or take the lock file, so the directory is byte-identical.
func TestGatherReadsClaimsReadOnly(t *testing.T) {
	f := newFixture(t)
	noGh(t)
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	root, err := filepath.EvalSymlinks(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	common := gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	dir := filepath.Join(common, "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(pid int, started int64) {
		body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=main\nagent=claude\n", pid, started, root)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(pid)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(sleep.Process.Pid, time.Now().Unix())
	write(sleep.Process.Pid+1_000_000, 1) // no such pid: stale
	before := snapshotDir(t, dir)

	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: t.TempDir(), OwnRepo: "o/r", Claim: sessionclaim.Claim{PID: os.Getpid()}})
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	r := Classify(s, Options{Now: later()})
	var rootRow *Row
	for i, row := range r.Rows {
		if row.Kind == KindWorktree && resolve(row.Path) == root {
			rootRow = &r.Rows[i]
		}
	}
	if rootRow == nil {
		t.Fatalf("no root row%s", dumpRows(r.Rows))
	}
	if rootRow.Status != StatusInUse || !slices.Contains(reasonCodes(rootRow.Blockers), BlockHeld) {
		t.Fatalf("root row %s blockers %v, want in_use with %q", rootRow.Status, reasonCodes(rootRow.Blockers), BlockHeld)
	}
	after := snapshotDir(t, dir)
	if len(before) != len(after) {
		t.Fatalf("claims directory changed: before %v after %v", keys(before), keys(after))
	}
	for k, v := range before {
		if !bytes.Equal([]byte(v), []byte(after[k])) {
			t.Fatalf("claims file %s changed", k)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, strings.TrimPrefix(k, "/"))
	}
	slices.Sort(out)
	return out
}

// TestGatherNotARepoIsAnError runs Gather outside any repository. Why: with
// no repository there is nothing to classify; that is exit 1, an error, not
// an empty list that reads as "nothing to clean".
func TestGatherNotARepoIsAnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := Gather(Git{Dir: dir}, GatherInputs{CWD: dir}); err == nil {
		t.Fatal("Gather outside a repository: want an error")
	}
}

// TestGatherSetsFetchedAtAndParsesOwnRepo fetches and reads the own repo
// from an origin URL. Why: the header shows the base's age from the fetch
// time, and an origin whose URL names no github owner/repo must leave PR
// facts unknown (gh unavailable), never "no PRs".
func TestGatherSetsFetchedAtAndParsesOwnRepo(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	stubGh(t, func(string, string) ([]byte, error) {
		t.Error("gh must not run without an own repo")
		return nil, nil
	})
	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Fetch.OK || s.Base.FetchedAt.IsZero() || !s.Base.FetchedAt.Equal(s.Fetch.At) {
		t.Fatalf("fetch %+v, base fetched at %v", s.Fetch, s.Base.FetchedAt)
	}
	if s.OwnRepo != "" {
		t.Fatalf("OwnRepo = %q for a local-path origin, want empty", s.OwnRepo)
	}
	if gh := Classify(s, Options{Now: later()}).GH; gh != GHUnavailable {
		t.Fatalf("GH = %q, want %q", gh, GHUnavailable)
	}
}

// TestOwnRepoFromURL checks the github owner/repo forms of an origin URL.
// Why: the own repo filters fork PRs out; a wrong parse would keep a fork's
// PR as evidence or drop the repo's own open-PR veto.
func TestOwnRepoFromURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/Owner/Repo.git":   "Owner/Repo",
		"https://github.com/owner/repo":       "owner/repo",
		"git@github.com:owner/repo.git":       "owner/repo",
		"ssh://git@github.com/owner/repo.git": "owner/repo",
		"https://gitlab.com/owner/repo.git":   "",
		"/tmp/origin.git":                     "",
		"https://github.com/owner":            "",
		"https://github.com/a/b/c":            "",
	} {
		if got := ownRepoFromURL(url); got != want {
			t.Errorf("ownRepoFromURL(%q) = %q, want %q", url, got, want)
		}
	}
}
