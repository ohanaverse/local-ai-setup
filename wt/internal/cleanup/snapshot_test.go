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

// TestGatherWithoutOriginSkipsFetch gathers, with a fetch asked for, in a
// repository that has no origin remote. Why: the base is then the local
// fallback and there is nothing to fetch; the header must say the fetch was
// skipped and why, and the JSON must leave `fetch` out as for no fetch at
// all, never print a failure or an ok.
func TestGatherWithoutOriginSkipsFetch(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "remove", "origin")
	noGh(t)
	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Fetch: true, OriginRows: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Base.Fallback || s.Fetch.Skipped != "no remotes" || !s.Base.FetchedAt.IsZero() {
		t.Fatalf("base %+v fetch %+v, want fallback base, skipped fetch, no fetched_at", s.Base, s.Fetch)
	}
	r := Classify(s, Options{Now: later()})
	header := strings.Join(HeaderLines(r, time.Now()), "\n")
	if !strings.Contains(header, "fetch: skipped (no remotes)") || strings.Contains(header, "FAILED") {
		t.Fatalf("header:\n%s", header)
	}
	raw, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"fetch"`) || strings.Contains(string(raw), `"fetched_at"`) {
		t.Fatalf("JSON carries a fetch or fetched_at for a skipped fetch:\n%s", raw)
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

// TestGatherReportsAnEarlierFetchWhenNotFetching gathers with Fetch false but
// the first run's result in Fetched. What: the snapshot carries it, so an
// origin row stays proven. Why: the tick list re-reads the repository after a
// removal without fetching; without the carried result every origin row would
// turn "not fetched" and lock after the first run.
func TestGatherReportsAnEarlierFetchWhenNotFetching(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Push("topic")
	tip := f.git("rev-parse", "main")
	mergedPRStub(t, "topic", 8, tip, tip)
	stubGhBase(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	first, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Fetch: true, OriginRows: true})
	if err != nil {
		t.Fatal(err)
	}
	again, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Fetched: first.Fetch, OriginRows: true})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Fetch.OK || !again.Fetch.At.Equal(first.Fetch.At) {
		t.Fatalf("fetch %+v, want the first run's %+v", again.Fetch, first.Fetch)
	}
	if row := findRow(t, Classify(again, Options{Now: later()}), KindRemote, "topic"); row.Status != StatusProven {
		t.Fatalf("origin row is %s after a fetch-less re-read, want proven", row.Status)
	}
}

// TestGatherAsksForTheBasePRsOnlyWhenOriginRowsAreWanted gathers a repo with
// one merged origin branch, with and without GatherInputs.OriginRows. What:
// without it no `gh pr list --base` call is made and BasePRs is empty; with it
// the branch is asked about once. Why: the query is the live `gh pr list
// --base`, one gh call per origin candidate, so Gather makes it only when
// OriginRows is set — callers that show or act on an origin row (the `wt
// worktrees` list, `wt remove origin/...`) pass OriginRows and pay it, while
// `wt remove <local branch>` never touches one and must not (Rescan re-asks
// before any delete either way).
func TestGatherAsksForTheBasePRsOnlyWhenOriginRowsAreWanted(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Push("topic")
	stubGh(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	for _, want := range []bool{false, true} {
		var calls []string
		stubGhBase(t, func(_, b string) ([]byte, error) { calls = append(calls, b); return []byte("[]"), nil })
		s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", OriginRows: want})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(calls) > 0; got != want || (len(s.BasePRs) > 0) != want {
			t.Fatalf("OriginRows=%v: base calls %v, BasePRs %v", want, calls, s.BasePRs)
		}
	}
}
