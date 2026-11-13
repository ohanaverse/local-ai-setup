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

// procStart returns pid's start time in epoch seconds — what a claim records
// (docs/contracts/session-claims.md: equal to the real start within 5 s). A
// fixture "other session" must read live under that rule, and this package
// cannot fake sessionclaim's process seams (unexported, and this change adds
// no exported ones), so the claim names a real child's real start time, read
// from ps's lstart in the local zone. (Shape: rootclaim's holdlaunch_test.)
func procStart(t *testing.T, pid int) int64 {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C") // English month names for the parse
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps lstart for %d: %v", pid, err)
	}
	start, err := time.ParseInLocation(time.ANSIC, strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		t.Fatalf("parse lstart %q: %v", out, err)
	}
	return start.Unix()
}

// holderClaim spawns a short-lived real process and claims path for it under
// commonDir — a fixture "other session" that reads live under the claim
// contract's liveness rule (real pid, real start time, never manufactured).
// The child and its claim are cleaned up. (Shape: rootclaim's holdlaunch_test.)
func holderClaim(t *testing.T, commonDir, path, branch, agent string) sessionclaim.Claim {
	t.Helper()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	c := sessionclaim.Claim{PID: child.Process.Pid, Started: procStart(t, child.Process.Pid), Path: path, Branch: branch, Agent: agent}
	release, err := sessionclaim.Hold(commonDir, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return c
}

// TestHeldLabelOnLinkedWorktree claims a linked worktree for another live
// session (a real child process under the claim contract) and classifies the
// repository. Why: held is a label, not a blocker — the row carries the held
// reason with the claim description (who holds the checkout) and no held
// blocker, the tag names the session, and HeldOn is the read-only lookup
// behind it.
func TestHeldLabelOnLinkedWorktree(t *testing.T) {
	f := newFixture(t)
	noGh(t)
	wt := f.Worktree("topic")
	common := gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	c := holderClaim(t, common, wt, "topic", "codex")
	s := gather(t, f)
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	var held []string
	for _, reason := range row.Reasons {
		if reason.Code == ReasonHeld {
			held = append(held, reason.Detail)
		}
	}
	if len(held) != 1 || !strings.Contains(held[0], fmt.Sprintf("codex (pid %d, ", c.PID)) || !strings.Contains(held[0], "started on topic)") {
		t.Fatalf("held reasons %q, want one claim description naming codex pid %d on topic%s", held, c.PID, dumpRows(r.Rows))
	}
	if slices.Contains(reasonCodes(row.Blockers), ReasonHeld) {
		t.Fatalf("blockers %v, want held as a label only%s", reasonCodes(row.Blockers), dumpRows(r.Rows))
	}
	if hs := s.HeldOn(row.Path); len(hs) != 1 || hs[0].PID != c.PID {
		t.Fatalf("HeldOn(%q) = %+v, want the one claim (pid %d)", row.Path, hs, c.PID)
	}
	if !slices.ContainsFunc(RowTags(row), func(tg Tag) bool {
		return strings.Contains(tg.Text, fmt.Sprintf("held by session codex (pid %d, ", c.PID))
	}) {
		t.Fatalf("tags %v, want one naming the holding session", RowTags(row))
	}
}

// TestHeldWorktreeNeverProven holds a worktree another live session claimed
// whose branch is as merged as a row can be (the base's own commit, old
// enough to trust): the held label alone must floor it at less_sure — a held
// row is never proven — while the same snapshot with the claims cleared reads
// proven. Why: proven says removing it loses nothing; a live session in the
// directory says otherwise.
func TestHeldWorktreeNeverProven(t *testing.T) {
	f := newFixture(t)
	noGh(t)
	wt := f.Worktree("topic")
	common := gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	holderClaim(t, common, wt, "topic", "codex")
	s := gather(t, f)
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	if row.Status != StatusLessSure || row.Tickable() {
		t.Fatalf("held row: status %s tickable %v, want less_sure and not tickable%s", row.Status, row.Tickable(), dumpRows(r.Rows))
	}
	if !slices.Contains(reasonCodes(row.Reasons), ReasonHeld) {
		t.Fatalf("reasons %v, want held%s", reasonCodes(row.Reasons), dumpRows(r.Rows))
	}
	bare := s
	bare.Claims = nil
	if row := findRow(t, Classify(bare, Options{Now: later()}), KindWorktree, "topic"); row.Status != StatusProven {
		t.Fatalf("the same row without the claims reads %s, want proven: held must be the only floor", row.Status)
	}
}

// TestHeldDoesNotMaskUniqueWork holds a dirty worktree another live session
// claimed. Why: held is a floor, never an override — uncommitted work is
// unique_work whatever else is true, and the row carries both reasons (dirty
// and the held label) rather than letting the label hide work at risk.
func TestHeldDoesNotMaskUniqueWork(t *testing.T) {
	f := newFixture(t)
	noGh(t)
	wt := f.Worktree("topic")
	f.DirtyIn(wt, "notes.txt")
	common := gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	holderClaim(t, common, wt, "topic", "codex")
	s := gather(t, f)
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	codes := reasonCodes(row.Reasons)
	if row.Status != StatusUniqueWork || !slices.Contains(codes, ReasonDirty) || !slices.Contains(codes, ReasonHeld) {
		t.Fatalf("status %s reasons %v, want unique_work carrying dirty and held%s", row.Status, codes, dumpRows(r.Rows))
	}
}

// TestGatherReadsClaimsReadOnly claims a linked worktree for another live
// session (a real child under the claim contract) and leaves a stale and a
// malformed claim file beside it — written after the claim, because Hold
// prunes stale files and Gather must not — then gathers and classifies. Why:
// gathering reads claims without writing or deleting any: it must see the
// holder (the held label) yet leave the directory byte-identical, stale and
// malformed files included. (Rewritten for held as a label.)
func TestGatherReadsClaimsReadOnly(t *testing.T) {
	f := newFixture(t)
	noGh(t)
	wt := f.Worktree("topic")
	common := gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	c := holderClaim(t, common, wt, "topic", "codex")
	dir := filepath.Join(common, "wt-sessions")
	stale := filepath.Join(dir, fmt.Sprint(c.PID+1_000_000)) // no such pid: stale
	malformed := filepath.Join(dir, fmt.Sprint(c.PID+2_000_000))
	if err := os.WriteFile(stale, []byte(fmt.Sprintf("pid=%d\nstarted=1\npath=%s\nbranch=main\nagent=claude\n", c.PID+1_000_000, wt)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(malformed, []byte("pid=not-a-number\nstarted=soon\npath=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, dir)

	s := gather(t, f)
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	if !slices.Contains(reasonCodes(row.Reasons), ReasonHeld) {
		t.Fatalf("reasons %v, want the held label: the claims were read%s", reasonCodes(row.Reasons), dumpRows(r.Rows))
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
	for _, p := range []string{stale, malformed} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("claims file %s is gone: gathering never prunes or deletes", p)
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
// the base-PR question before a delete only for an origin-branch target — its
// kind-remote path runs LookupPRs/LookupBaseOf; a local-branch target copies and
// asks nothing).
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

// TestRootRowCarriesDriftReason pins the root row's drift label: a root
// checkout off its default branch carries ReasonRootDrift on its row —
// rootclaim.Drift's line as the detail — and a detached root reads `root is
// detached`; only the root's own row carries it. Why: the row is what says
// the root needs `wt restore-root`, and the detail is Drift's text so the
// list and the launch warning read alike. It is raised soft like held: a
// label in Reasons, never a blocker — the root's refusal to be removed stays
// BlockPrimary's alone.
func TestRootRowCarriesDriftReason(t *testing.T) {
	t.Run("root on another branch", func(t *testing.T) {
		f := newFixture(t)
		noGh(t)
		f.git("checkout", "-b", "topic")
		other := f.Worktree("other")
		s := gather(t, f)
		r := Classify(s, Options{Now: later()})
		row := primaryRow(t, r)
		if got := reasonDetail(row.Reasons, ReasonRootDrift); got != "root is on topic, not main" {
			t.Fatalf("root drift detail %q, want %q%s", got, "root is on topic, not main", dumpRows(r.Rows))
		}
		if slices.Contains(reasonCodes(row.Blockers), ReasonRootDrift) {
			t.Fatalf("blockers %v, want root_drift as a label only%s", reasonCodes(row.Blockers), dumpRows(r.Rows))
		}
		if got := reasonDetail(findRow(t, r, KindWorktree, "other").Reasons, ReasonRootDrift); got != "" {
			t.Errorf("linked worktree %s carries the drift %q, want the root's alone", other, got)
		}
	})
	t.Run("detached root", func(t *testing.T) {
		f := newFixture(t)
		noGh(t)
		f.git("checkout", "--detach")
		other := f.Worktree("other")
		s := gather(t, f)
		r := Classify(s, Options{Now: later()})
		row := primaryRow(t, r)
		if got := reasonDetail(row.Reasons, ReasonRootDrift); got != "root is detached" {
			t.Fatalf("root drift detail %q, want %q%s", got, "root is detached", dumpRows(r.Rows))
		}
		if slices.Contains(reasonCodes(row.Blockers), ReasonRootDrift) {
			t.Fatalf("blockers %v, want root_drift as a label only%s", reasonCodes(row.Blockers), dumpRows(r.Rows))
		}
		if got := reasonDetail(findRow(t, r, KindWorktree, "other").Reasons, ReasonRootDrift); got != "" {
			t.Errorf("linked worktree %s carries the drift %q, want the root's alone", other, got)
		}
	})
}

// TestRootDriftNotEmittedWithoutDefault (Review Focus 1) takes origin/HEAD
// away from a repository whose root is off its main branch — on a branch and
// detached, both pinned — and requires every row to still be listed with no
// drift label on any of them. Why: with no known default there is nothing to
// be off of. The default must come from origin/HEAD alone
// (worktree.DefaultBranch), never the cleanup base's main/master fallbacks,
// or a repository without origin/HEAD would warn of a drift nobody
// established; detached with no default invents nothing either.
func TestRootDriftNotEmittedWithoutDefault(t *testing.T) {
	for name, detach := range map[string]bool{"root on another branch": false, "detached root": true} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			noGh(t)
			if detach {
				f.git("checkout", "--detach")
			} else {
				f.git("checkout", "-b", "topic")
			}
			f.Worktree("other")
			f.git("remote", "set-head", "--delete", "origin")
			s := gather(t, f)
			r := Classify(s, Options{Now: later()})
			primaryRow(t, r) // the root row is still listed
			findRow(t, r, KindWorktree, "other")
			for _, row := range r.Rows {
				if got := reasonDetail(row.Reasons, ReasonRootDrift); got != "" {
					t.Errorf("row %s carries drift %q with no default known%s", row.Name, got, dumpRows(r.Rows))
				}
			}
		})
	}
}
