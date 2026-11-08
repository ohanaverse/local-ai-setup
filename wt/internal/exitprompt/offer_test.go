package exitprompt

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// failReader fails the test if it is read: a silent Offer must not ask.
type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Error("Offer read the answer, but it should have stayed silent")
	return 0, errors.New("unexpected read")
}

// hookReader runs fn on the first read, then answers with ans.
type hookReader struct {
	fn   func()
	ans  string
	done bool
}

func (r *hookReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		if r.fn != nil {
			r.fn()
		}
	}
	if r.ans == "" {
		return 0, errors.New("EOF")
	}
	n := copy(p, r.ans)
	r.ans = r.ans[n:]
	return n, nil
}

type noProcs struct{ procs []removal.Proc }

func (n noProcs) InDir(string) ([]removal.Proc, error) { return n.procs, nil }

// offerCase is a fresh repository (with a bare origin unless noRemote), the
// process chdir'd into its root, a worktree created and recorded like a -W
// launch does, and an Env that gathers with fetch off.
type offerCase struct {
	t          *testing.T
	root, path string
	branch     string
	env        Env
}

func newOfferCase(t *testing.T, noRemote bool) *offerCase {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
	root := testRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".gitignore")
	git(t, root, "commit", "-q", "-m", "ignore logs")
	if !noRemote {
		bare := filepath.Join(t.TempDir(), "origin.git")
		git(t, root, "clone", "-q", "--bare", root, bare)
		git(t, root, "remote", "add", "origin", bare)
		git(t, root, "fetch", "-q", "origin")
		git(t, root, "branch", "-q", "--set-upstream-to=origin/main", "main")
		git(t, root, "remote", "set-head", "origin", "main")
	}
	t.Chdir(root)
	return &offerCase{t: t, root: root, branch: "topic", env: Env{
		Inputs: func(cwd string) (cleanup.GatherInputs, error) {
			return cleanup.GatherInputs{CWD: cwd, OwnRepo: "o/r", Parallel: 1,
				Claim: sessionclaim.Claim{PID: os.Getpid(), Agent: "test"}}, nil
		},
		Procs: noProcs{},
	}}
}

// create makes the worktree for c.branch and records it.
func (c *offerCase) create() {
	c.t.Helper()
	p, err := worktree.EnsureForName(c.root, c.branch)
	if err != nil {
		c.t.Fatal(err)
	}
	c.path = p
	if err := Record(p, c.branch); err != nil {
		c.t.Fatal(err)
	}
}

func (c *offerCase) branchExists() bool {
	return git(c.t, c.root, "branch", "--list", c.branch) != ""
}

func (c *offerCase) offer(in any, tty bool) string {
	c.t.Helper()
	var out bytes.Buffer
	var r interface{ Read([]byte) (int, error) }
	switch v := in.(type) {
	case string:
		r = strings.NewReader(v)
	case interface{ Read([]byte) (int, error) }:
		r = v
	}
	Offer(c.env, r, &out, tty)
	return out.String()
}

func (c *offerCase) intact() bool {
	_, err := os.Stat(c.path)
	return err == nil && c.branchExists()
}

// TestOfferUntouchedWorktreeRemovesWorktreeAndBranchOnYes checks the happy
// path: an untouched created worktree is offered, and "y" removes both the
// directory and the branch through the engine, printing its headline.
func TestOfferUntouchedWorktreeRemovesWorktreeAndBranchOnYes(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	out := c.offer("y", true)
	if _, err := os.Stat(c.path); !os.IsNotExist(err) {
		t.Errorf("worktree still there after y (stat err %v)\n%s", err, out)
	}
	if c.branchExists() {
		t.Errorf("branch %s survived a y\n%s", c.branch, out)
	}
	if !strings.Contains(out, "was created for this session and nothing changed in it. Remove it and its branch topic?") ||
		!strings.Contains(out, "worktree removed, branch deleted") {
		t.Errorf("output lacks the question or the engine headline:\n%s", out)
	}
}

// TestOfferAlsoWorksOnceTheSessionOutlivesTheMinAge checks a long session:
// past the 10 minute floor the engine calls the untouched branch proven, and
// the offer must still be made and carried out, since most sessions run longer.
func TestOfferAlsoWorksOnceTheSessionOutlivesTheMinAge(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	c.env.Now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	out := c.offer("y", true)
	if _, err := os.Stat(c.path); !os.IsNotExist(err) || c.branchExists() {
		t.Errorf("a long untouched session was not removed on y:\n%s", out)
	}
}

// TestOfferDeclineKeepsEverything checks n, Enter and the end of input all
// leave the worktree and the branch, because only an explicit y removes.
func TestOfferDeclineKeepsEverything(t *testing.T) {
	for name, ans := range map[string]string{"n": "n", "enter": "\n", "eof": ""} {
		t.Run(name, func(t *testing.T) {
			c := newOfferCase(t, false)
			c.create()
			out := c.offer(ans, true)
			if !strings.Contains(out, "Remove it and its branch topic?") {
				t.Errorf("no question asked:\n%s", out)
			}
			if !c.intact() {
				t.Errorf("answer %q removed something", ans)
			}
		})
	}
}

// TestOfferIsSilentWhenTouched checks each way a session can touch the
// worktree (an untracked file, a modified tracked file, a new commit, an
// ignored file, a live process) silences the offer without reading input, so
// work is never put one keypress from deletion.
func TestOfferIsSilentWhenTouched(t *testing.T) {
	cases := map[string]func(c *offerCase){
		"untracked file": func(c *offerCase) { os.WriteFile(filepath.Join(c.path, "new.txt"), []byte("x"), 0o644) },
		"modified tracked file": func(c *offerCase) {
			os.WriteFile(filepath.Join(c.path, ".gitignore"), []byte("*.log\n*.tmp\n"), 0o644)
		},
		"new commit":   func(c *offerCase) { git(c.t, c.path, "commit", "--allow-empty", "-q", "-m", "work") },
		"ignored file": func(c *offerCase) { os.WriteFile(filepath.Join(c.path, "debug.log"), []byte("x"), 0o644) },
		"running process": func(c *offerCase) {
			c.env.Procs = noProcs{procs: []removal.Proc{{PID: 42, Command: "claude"}}}
		},
	}
	for name, touch := range cases {
		t.Run(name, func(t *testing.T) {
			c := newOfferCase(t, false)
			c.create()
			touch(c)
			if out := c.offer(failReader{t}, true); out != "" {
				t.Errorf("expected no output, got:\n%s", out)
			}
			if !c.intact() {
				t.Error("something was removed")
			}
		})
	}
}

// TestOfferIsSilentAfterACommitUndoneByAReset checks that a commit undone by a
// hard reset back to the creation tip still counts as touched. The tree is
// clean and the tip equals the creation tip, but deleting the branch deletes
// its reflog, which is the only thing that still reaches the discarded commit;
// a session that made and discarded work must not be offered a removal that
// orphans it.
func TestOfferIsSilentAfterACommitUndoneByAReset(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	tip := git(t, c.path, "rev-parse", "HEAD")
	git(t, c.path, "commit", "--allow-empty", "-q", "-m", "oops")
	git(t, c.path, "reset", "-q", "--hard", tip)
	if out := c.offer(failReader{t}, true); out != "" {
		t.Errorf("offered after a reset to the creation tip:\n%s", out)
	}
	if !c.intact() {
		t.Error("worktree or branch was removed")
	}
}

// TestOfferIsSilentWhenTheReflogCannotBeRead checks the fail-closed side of
// the reflog rule: with reflogs off the branch has no history to compare, so
// "nothing happened" cannot be shown and the worktree is kept silently.
func TestOfferIsSilentWhenTheReflogCannotBeRead(t *testing.T) {
	c := newOfferCase(t, false)
	git(t, c.root, "config", "core.logAllRefUpdates", "false")
	c.create()
	if out := c.offer(failReader{t}, true); out != "" {
		t.Errorf("offered with no reflog to compare:\n%s", out)
	}
	if !c.intact() {
		t.Error("worktree or branch was removed")
	}
}

// TestReflogUntouched checks the rule itself: the count recorded at creation
// must still be the count, and a missing baseline never passes.
func TestReflogUntouched(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	rec, _ := Recorded()
	if ok, why := ReflogUntouched(rec); !ok {
		t.Fatalf("a fresh worktree is touched: %s", why)
	}
	git(t, c.path, "commit", "--allow-empty", "-q", "-m", "x")
	if ok, _ := ReflogUntouched(rec); ok {
		t.Error("a new commit leaves the reflog untouched")
	}
	rec.Reflog = 0
	if ok, _ := ReflogUntouched(rec); ok {
		t.Error("a record with no reflog baseline passes")
	}
}

// TestOfferWithoutARemoteStillOffers checks a repository with no origin, where
// the classifier adds base_is_local, is still offered: only the fresh-branch
// style reasons are tolerated, and this is one.
func TestOfferWithoutARemoteStillOffers(t *testing.T) {
	c := newOfferCase(t, true)
	c.create()
	if out := c.offer("n", true); !strings.Contains(out, "Remove it and its branch") {
		t.Errorf("not offered without a remote:\n%s", out)
	}
}

// TestOfferIsSilentWhenTheBranchStartedAheadOfTheBase checks a worktree made
// from a HEAD carrying commits origin/main lacks is not offered: removing the
// branch would drop commits that exist nowhere else.
func TestOfferIsSilentWhenTheBranchStartedAheadOfTheBase(t *testing.T) {
	c := newOfferCase(t, false)
	git(t, c.root, "commit", "--allow-empty", "-q", "-m", "local only")
	c.create()
	if out := c.offer(failReader{t}, true); out != "" {
		t.Errorf("expected no output, got:\n%s", out)
	}
	if !c.intact() {
		t.Error("something was removed")
	}
}

// TestOfferSkipsWhenTheTipMovesAfterTheQuestion checks the engine's
// compare-and-swap still guards a "y": a commit made between the question and
// the removal is reported as skipped and nothing is deleted.
func TestOfferSkipsWhenTheTipMovesAfterTheQuestion(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	in := &hookReader{ans: "y", fn: func() {
		git(t, c.path, "commit", "--allow-empty", "-q", "-m", "late work")
	}}
	out := c.offer(in, true)
	if !strings.Contains(out, "skipped_changed") {
		t.Errorf("expected the engine's skipped_changed, got:\n%s", out)
	}
	if !c.intact() {
		t.Error("worktree or branch removed although the tip moved")
	}
}

// TestOfferKeepAndNoTerminalAreSilent checks the three early exits (the keep
// setting, no terminal, nothing recorded) print nothing and read nothing.
func TestOfferKeepAndNoTerminalAreSilent(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	c.env.Keep = true
	if out := c.offer(failReader{t}, true); out != "" {
		t.Errorf("Keep printed %q", out)
	}
	c.env.Keep = false
	if out := c.offer(failReader{t}, false); out != "" {
		t.Errorf("no terminal printed %q", out)
	}
	Reset()
	if out := c.offer(failReader{t}, true); out != "" {
		t.Errorf("nothing recorded printed %q", out)
	}
	if !c.intact() {
		t.Error("something was removed")
	}
}

// TestOfferReportsACheckErrorAsANote checks a failing gather becomes the one
// documented note line, keeps the worktree and does not panic.
func TestOfferReportsACheckErrorAsANote(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	c.env.Inputs = func(string) (cleanup.GatherInputs, error) {
		return cleanup.GatherInputs{}, errors.New("registry unreadable")
	}
	out := c.offer(failReader{t}, true)
	want := "wt: note: could not check " + c.path + " for removal (registry unreadable); kept\n"
	if out != want {
		t.Errorf("output %q, want %q", out, want)
	}
	if !c.intact() {
		t.Error("something was removed")
	}
}

// TestEligibleNamesTheFailingCondition checks each condition of the
// predicate fails on its own with its own reason, so a log line says why a
// worktree was kept; the base preview passes.
func TestEligibleNamesTheFailingCondition(t *testing.T) {
	cr := Created{Path: "/w", Branch: "topic", Tip: "abc"}
	base := func() removal.ItemPreview {
		return removal.ItemPreview{
			Approval:        removal.ApprovalNeedsConfirm,
			CanDeleteBranch: true,
			Fresh: cleanup.Row{Kind: cleanup.KindWorktree, Status: cleanup.StatusLessSure, Evidence: cleanup.EvSameCommit,
				Ref: "refs/heads/topic", Branch: "topic", Tip: "abc",
				Reasons: []cleanup.Reason{{Code: cleanup.ReasonFreshBranch}, {Code: cleanup.ReasonBaseIsLocal}}},
		}
	}
	if ok, why := Eligible(base(), cr); !ok {
		t.Fatalf("base preview not eligible: %s", why)
	}
	mut := map[string]func(p *removal.ItemPreview){
		"refused":      func(p *removal.ItemPreview) { p.Approval = removal.ApprovalRefused },
		"other branch": func(p *removal.ItemPreview) { p.Fresh.Branch = "other"; p.Fresh.Ref = "refs/heads/other" },
		"moved tip":    func(p *removal.ItemPreview) { p.Fresh.Tip = "def" },
		"loss lines":   func(p *removal.ItemPreview) { p.LossLines = []string{"1 ignored"} },
		"losses":       func(p *removal.ItemPreview) { p.Fresh.Losses.Dirty = true },
		"process":      func(p *removal.ItemPreview) { p.Procs = []removal.Proc{{PID: 1}} },
		"blocker":      func(p *removal.ItemPreview) { p.Fresh.Blockers = []cleanup.Reason{{Code: "x"}} },
		"unknown":      func(p *removal.ItemPreview) { p.Fresh.Unknown = true },
		"unique work":  func(p *removal.ItemPreview) { p.Fresh.Status = cleanup.StatusUniqueWork },
		"other reason": func(p *removal.ItemPreview) {
			p.Fresh.Reasons = append(p.Fresh.Reasons, cleanup.Reason{Code: "open_pr"})
		},
		"evidence":      func(p *removal.ItemPreview) { p.Fresh.Evidence = cleanup.EvTreesMatch },
		"cannot delete": func(p *removal.ItemPreview) { p.CanDeleteBranch = false },
	}
	seen := map[string]string{}
	for name, f := range mut {
		p := base()
		f(&p)
		ok, why := Eligible(p, cr)
		if ok || why == "" {
			t.Errorf("%s: ok=%v why=%q, want a refusal with a reason", name, ok, why)
			continue
		}
		if prev, dup := seen[why]; dup && !(name == "loss lines" || name == "losses" || prev == "loss lines" || prev == "losses") {
			t.Errorf("%s and %s share the reason %q", name, prev, why)
		}
		seen[why] = name
	}
}

// TestEligibleRejectsAPreservedRow checks that a worktree a rescue record
// covers (Preserved set) is still refused by the exit prompt when its raw
// Losses.Unpushed is 2 and no loss line was printed, with the "lose
// something" reason, while every other condition passes. Why: the prompt
// offers only a worktree that holds nothing at all, record or not; reading
// the effective losses here would delete a worktree whose commits only the
// record keeps.
func TestEligibleRejectsAPreservedRow(t *testing.T) {
	cr := Created{Path: "/w", Branch: "topic", Tip: "abc"}
	p := removal.ItemPreview{
		Approval:        removal.ApprovalNeedsConfirm,
		CanDeleteBranch: true,
		LossLines:       nil,
		Fresh: cleanup.Row{Kind: cleanup.KindWorktree, Status: cleanup.StatusLessSure, Evidence: cleanup.EvSameCommit,
			Ref: "refs/heads/topic", Branch: "topic", Tip: "abc",
			Reasons:   []cleanup.Reason{{Code: cleanup.ReasonFreshBranch}, {Code: cleanup.ReasonBaseIsLocal}},
			Losses:    cleanup.Losses{Unpushed: 2},
			Preserved: &cleanup.Preserved{Ref: "refs/wt/rescued/0123456789abcdef", DestBranch: "rescued/topic", DestPath: "/copy"}},
	}
	ok, why := Eligible(p, cr)
	if ok || why != "removing it would lose something" {
		t.Errorf("Eligible = %v, %q; want false, %q", ok, why, "removing it would lose something")
	}
}

// errProcs is a ProcFinder whose lookup fails, like lsof missing or timing out.
type errProcs struct{}

func (errProcs) InDir(string) ([]removal.Proc, error) { return nil, errors.New("lsof timed out") }

// TestOfferIsSilentWhenTheProcessCheckFails checks that a worktree whose
// running-process lookup failed is not offered: "nothing changed in it" is
// unverified, so the offer fails closed rather than deleting under a possible
// live process.
func TestOfferIsSilentWhenTheProcessCheckFails(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	c.env.Procs = errProcs{}
	if out := c.offer(failReader{t}, true); out != "" {
		t.Errorf("expected no output, got:\n%s", out)
	}
	if !c.intact() {
		t.Error("something was removed")
	}
}

// TestOfferFlushesTypeaheadJustBeforeTheQuestion checks the input flush runs
// after eligibility is known and before the question is read, so keys typed
// while the agent ran cannot answer it, and that it does not run when the
// offer stays silent.
func TestOfferFlushesTypeaheadJustBeforeTheQuestion(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	flushed := 0
	c.env.Flush = func() { flushed++ }
	in := &hookReader{ans: "n", fn: func() {
		if flushed != 1 {
			t.Errorf("flush ran %d times before the answer was read, want 1", flushed)
		}
	}}
	c.offer(in, true)
	if flushed != 1 {
		t.Errorf("flush ran %d times, want 1", flushed)
	}
	git(t, c.path, "commit", "--allow-empty", "-q", "-m", "work")
	flushed = 0
	c.offer(failReader{t}, true)
	if flushed != 0 {
		t.Errorf("flush ran for a silent offer")
	}
}

// TestLaunchedDropsTheRecordForAnotherDirectory checks that a creation record
// survives only a launch in (or under) the created worktree: a launch in the
// root or another worktree, as after the picker's new-worktree row, means the
// agent never ran there, so nothing is offered. An empty dir means the cwd.
func TestLaunchedDropsTheRecordForAnotherDirectory(t *testing.T) {
	c := newOfferCase(t, false)
	c.create()
	Launched(filepath.Join(c.path, "sub"))
	if _, ok := Recorded(); !ok {
		t.Fatal("a launch under the worktree dropped the record")
	}
	Launched(c.path)
	if _, ok := Recorded(); !ok {
		t.Fatal("a launch in the worktree dropped the record")
	}
	Launched(c.root)
	if _, ok := Recorded(); ok {
		t.Error("a launch in the root kept the record")
	}
	c.create()
	Launched("")
	if _, ok := Recorded(); ok {
		t.Error("a launch in the cwd (the root) kept the record")
	}
	c.create()
	t.Chdir(c.path)
	Launched("")
	if _, ok := Recorded(); !ok {
		t.Error("a launch in the cwd (the worktree) dropped the record")
	}
}

// TestOfferExplainsABranchKeptAfterTheWorktreeWasRemoved checks the engine's
// reason reaches the user in the one outcome where it matters most: "y"
// removed the worktree but the branch stage kept the branch (a case-only
// sibling appeared, or another worktree took the branch after eligibility saw
// a deletable one). Without the reason line the user watches work survive a
// yes to "remove it and its branch" with nothing saying why; cmd/wt/remove.go
// and the cleanup tick list print this outcome's reason unconditionally. The
// break this catches: gating the reason on the worktree outcome alone.
func TestOfferExplainsABranchKeptAfterTheWorktreeWasRemoved(t *testing.T) {
	cases := map[string]struct {
		touch   func(t *testing.T, c *offerCase)
		outcome string
		phrase  string
	}{
		"case-only sibling": {
			touch: func(t *testing.T, c *offerCase) {
				// Topic packed and topic loose at one commit (the
				// caseSiblings recipe in internal/removal): the branch stage
				// keeps the branch on every filesystem, and says delete by
				// hand (Ruling X).
				tip := git(t, c.root, "rev-parse", "refs/heads/topic")
				git(t, c.root, "pack-refs", "--all")
				git(t, c.root, "update-ref", "refs/heads/Topic", tip)
			},
			outcome: "retained_unknown",
			phrase:  "differing only in case",
		},
		"branch taken by the root": {
			touch: func(t *testing.T, c *offerCase) {
				// The root checks the branch out after the question, so the
				// branch stage finds it held and keeps it.
				git(t, c.root, "checkout", "-q", "--ignore-other-worktrees", "topic")
			},
			outcome: "retained_checked_out",
			phrase:  "held by another worktree",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newOfferCase(t, false)
			c.create()
			in := &hookReader{ans: "y", fn: func() { tc.touch(t, c) }}
			out := c.offer(in, true)
			if _, err := os.Stat(c.path); !os.IsNotExist(err) {
				t.Fatalf("worktree still there (stat err %v)\n%s", err, out)
			}
			if !strings.Contains(out, "worktree removed") || !strings.Contains(out, tc.outcome) {
				t.Errorf("output does not report a removed worktree and %s:\n%s", tc.outcome, out)
			}
			if !strings.Contains(out, "reason: ") || !strings.Contains(out, tc.phrase) {
				t.Errorf("output does not say why the branch survived (%q):\n%s", tc.phrase, out)
			}
			// Asserted on for-each-ref, never `branch --list`: on a
			// case-insensitive volume the sibling's ref file answers to both
			// spellings.
			refs := git(t, c.root, "for-each-ref", "--format=%(refname)", "refs/heads/")
			if !strings.Contains(refs, "refs/heads/topic") {
				t.Errorf("branch topic was deleted although the outcome kept it; refs:\n%s", refs)
			}
		})
	}
}
