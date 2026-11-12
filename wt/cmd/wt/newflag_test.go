package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// answerOnStdin makes os.Stdin a pipe that holds answer and ends, until the
// test ends: what a person typing one key on a terminal would send.
func answerOnStdin(t *testing.T, answer string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(answer); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

// captureNotes points osStderr at a buffer, where the question is printed.
func captureNotes(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := osStderr
	osStderr = &b
	t.Cleanup(func() { osStderr = old })
	return &b
}

// wantNothingCreated fails when name left a branch or a .worktrees/<name>
// directory in root.
func wantNothingCreated(t *testing.T, root, name string) {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "branch", "--list", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch %s exists: %s", name, out)
	}
	if fileExists(filepath.Join(root, ".worktrees", name)) {
		t.Errorf(".worktrees/%s exists", name)
	}
}

// setOriginDefault gives the repository at root an origin/HEAD naming main,
// the one thing the launch warnings' drift rule needs to know the default
// branch.
func setOriginDefault(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestWorktreeFlagUnknownNameWithoutTerminalCreatesAndSaysSo checks -W with a
// name that exists nowhere creates the branch and worktree even with no
// terminal, printing one informational line about the creation first and
// nothing else. Why: creation is not gated on a question any more — a script
// gets the worktree it named, and the line is what replaces the question
// ("creates and says so"); no warnings means no question on any path.
func TestWorktreeFlagUnknownNameWithoutTerminalCreatesAndSaysSo(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, false)
	notes := captureNotes(t)
	res := runPinnedLaunch(t, root, []string{"-W", "typo"})
	want := physical(t, filepath.Join(root, ".worktrees", "typo"))
	if res.err != nil || res.agentDir != want {
		t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, want)
	}
	if s := notes.String(); !strings.Contains(s, "creating branch typo and worktree") {
		t.Errorf("notes = %q; want the creation line", s)
	}
	if strings.Contains(notes.String(), "continue?") {
		t.Errorf("notes = %q; no warnings means no question", notes.String())
	}
}

// TestWorktreeFlagUnknownNameDoesNotAskOnATerminal checks the terminal half:
// an unknown name is created and launched with only the creation line — the
// create-it question is gone. Stdin is at end of input, so a question asked
// anyway would answer no and fail the launch check below, instead of reading
// the terminal this test runs from.
func TestWorktreeFlagUnknownNameDoesNotAskOnATerminal(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, true)
	answerOnStdin(t, "")
	notes := captureNotes(t)
	res := runPinnedLaunch(t, root, []string{"-W", "typo"})
	want := physical(t, filepath.Join(root, ".worktrees", "typo"))
	if res.err != nil || res.agentDir != want {
		t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, want)
	}
	if s := notes.String(); !strings.Contains(s, "creating branch typo and worktree") || strings.Contains(s, "continue?") {
		t.Errorf("notes = %q; want the creation line and no question", s)
	}
}

// TestWorktreeFlagNewCreatesWithoutAsking checks --new is still accepted and
// changes nothing: an unknown name is created and launched with the creation
// line and no question, --new or not. Stdin is at end of input, so a question
// asked anyway would answer no and fail the launch check below, instead of
// reading the terminal this test runs from. Why: --new must not become a
// usage error for scripts that still pass it.
func TestWorktreeFlagNewCreatesWithoutAsking(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, true)
	answerOnStdin(t, "")
	notes := captureNotes(t)
	res := runPinnedLaunch(t, root, []string{"-W", "fresh", "--new"})
	want := physical(t, filepath.Join(root, ".worktrees", "fresh"))
	if res.err != nil || res.agentDir != want {
		t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, want)
	}
	if s := notes.String(); strings.Contains(s, "[y/N]") || strings.Contains(s, "continue?") {
		t.Errorf("a question was printed: %q", s)
	}
}

// TestWorktreeFlagExistingNamesNeverAsk checks the names that are not a
// creation (an existing local branch with no worktree, an existing worktree)
// launch with no question and no terminal. The root's branch is not in this
// table any more: it launches in the root with the root warning
// (TestWorktreeFlagNamingTheRootBranchLaunchesWithWarning).
func TestWorktreeFlagExistingNamesNeverAsk(t *testing.T) {
	t.Run("local branch without a worktree", func(t *testing.T) {
		root := sharedRootRepo(t)
		if out, err := exec.Command("git", "-C", root, "branch", "topic").CombinedOutput(); err != nil {
			t.Fatalf("branch: %v\n%s", err, out)
		}
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"-W", "topic"})
		want := physical(t, filepath.Join(root, ".worktrees", "topic"))
		if res.err != nil || res.agentDir != want {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, want)
		}
	})
	t.Run("existing worktree", func(t *testing.T) {
		root := sharedRootRepo(t)
		linked := addLinkedWorktree(t, root, "feat")
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"-W", "feat"})
		if res.err != nil || res.agentDir != linked {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, linked)
		}
	})
}

// TestWorktreeFlagInvalidNameErrorsBeforeTheCreationLine checks a name the
// worktree plan rejects fails with that error before anything is announced,
// so the creation line never promises a worktree git would not make. (The
// default branch's remote form, origin/main, used to sit in this table; it is
// a valid name now — internal/worktree no longer refuses the default branch —
// and resolves like any other name.)
func TestWorktreeFlagInvalidNameErrorsBeforeTheCreationLine(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{name: strings.Repeat("a", 300), want: "too long"},
	} {
		t.Run(tc.name[:min(len(tc.name), 12)], func(t *testing.T) {
			root := sharedRootRepo(t)
			stubStdinTTY(t, true)
			notes := captureNotes(t)
			res := runPinnedLaunch(t, root, []string{"-W", tc.name})
			if res.err == nil {
				t.Fatal("the launch succeeded; want the name rejected")
			}
			if !strings.Contains(res.err.Error(), tc.want) {
				t.Errorf("err = %v; want it to say %q", res.err, tc.want)
			}
			if s := notes.String(); strings.Contains(s, "creating") || strings.Contains(s, "continue?") {
				t.Errorf("announced before rejecting: err=%v notes=%q", res.err, s)
			}
		})
	}
}

// TestNewFlagUsage checks --new is only meaningful with -W: alone, with --cwd
// or with --root it is a usage error and nothing launches; with an existing
// name it changes nothing.
func TestNewFlagUsage(t *testing.T) {
	for _, where := range [][]string{{"--new"}, {"--new", "--cwd"}, {"--new", "--root"}} {
		t.Run(strings.Join(where, " "), func(t *testing.T) {
			root := sharedRootRepo(t)
			stubStdinTTY(t, false)
			res := runPinnedLaunch(t, root, where)
			if res.err == nil || !strings.Contains(res.err.Error(), "--new") {
				t.Fatalf("err = %v; want a usage error naming --new", res.err)
			}
			if res.agentDir != "" {
				t.Errorf("the agent ran in %q", res.agentDir)
			}
		})
	}
	t.Run("existing name is a no-op", func(t *testing.T) {
		root := sharedRootRepo(t)
		linked := addLinkedWorktree(t, root, "feat")
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"-W", "feat", "--new"})
		if res.err != nil || res.agentDir != linked {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, linked)
		}
	})
}

// TestWorktreeFlagDeclinedAtTheWarningCreatesNothing checks a "no" at the one
// launch question makes the -W creation nothing: no branch, no .worktrees/<name>
// directory, and the agent never runs. The warning that raises the question here
// is root drift (root on topic, default main), with the restore-root hint riding
// along. Why: "warn but never prevent" turns on this — the question's no is a
// choice, not a failure (nil error, exit 0), and a declined creation leaves no
// trace for a later cleanup to find. This is the surviving half of the deleted
// exit-prompt record test's "declined" case (which asserted the same
// non-creation through the now-removed Recorded()).
func TestWorktreeFlagDeclinedAtTheWarningCreatesNothing(t *testing.T) {
	root := sharedRootRepo(t)
	// Root drift (root on topic, default main) is the warning that makes
	// a creation ask; the trailing restore-root hint rides along.
	setOriginDefault(t, root)
	cgit(t, root, "switch", "-c", "topic")
	stubStdinTTY(t, true)
	answerOnStdin(t, "n\n")
	notes := captureNotes(t)
	res := runPinnedLaunch(t, root, []string{"-W", "typo"})
	if res.err != nil {
		t.Fatalf("err = %v; a no is a choice, not a failure", res.err)
	}
	s := notes.String()
	if !strings.Contains(s, "continue? [Y/n]") || !strings.Contains(s, "restore-root") {
		t.Errorf("notes = %q; want the question and the restore-root hint", s)
	}
	if res.agentDir != "" {
		t.Errorf("the agent ran in %q despite the no", res.agentDir)
	}
	wantNothingCreated(t, root, "typo")
}

// TestDashWVanishedWorktreeRefusesNamingGitWorktreePrune checks a name whose
// registered worktree directory is gone (git lists the entry, prunable) is
// refused before anything is announced or asked. The plan is a reuse — it
// creates nothing — yet the missing path used to route it to ForNew, which
// announced `creating worktree <p> for branch <b>` and then created nothing:
// the agent died at chdir. The refusal names the fix (git worktree prune frees
// the registration; wt never prunes on its own).
func TestDashWVanishedWorktreeRefusesNamingGitWorktreePrune(t *testing.T) {
	root := sharedRootRepo(t)
	linked := addLinkedWorktree(t, root, "feat")
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	notes := captureNotes(t)
	stubStdinTTY(t, false)
	res := runPinnedLaunch(t, root, []string{"-W", "feat"})
	if res.err == nil || !strings.Contains(res.err.Error(), "git worktree prune") || !strings.Contains(res.err.Error(), "gone") {
		t.Fatalf("err = %v; want the vanished-worktree refusal naming git worktree prune", res.err)
	}
	if strings.Contains(notes.String(), "creating") {
		t.Errorf("a creation was announced for a plan that creates nothing: %q", notes.String())
	}
}

// TestDashWTrackingNameWithLocalBranchRefusesBeforeAnnouncing checks
// `wt -W origin/foo` with a local foo (no worktree) is refused at the name —
// before any creation line or question. The plan is a tracking creation whose
// commit point would refuse `local branch "foo" already exists — cannot create
// from remote "origin/foo"` only after `creating worktree <p> for branch foo
// tracking origin/foo` has already promised it; the failure is knowable at
// classify time. The name is not reinterpreted as reusing local foo (wt -W foo
// covers that).
func TestDashWTrackingNameWithLocalBranchRefusesBeforeAnnouncing(t *testing.T) {
	root := sharedRootRepo(t)
	// origin/foo exists as a remote-tracking ref and its local branch foo
	// exists too, held by no worktree.
	cgit(t, root, "remote", "add", "origin", filepath.Join(root, "remote.git"))
	cgit(t, root, "update-ref", "refs/remotes/origin/foo", "HEAD")
	cgit(t, root, "branch", "foo")
	notes := captureNotes(t)
	stubStdinTTY(t, false)
	res := runPinnedLaunch(t, root, []string{"-W", "origin/foo"})
	if res.err == nil || !strings.Contains(res.err.Error(), `local branch "foo" already exists`) || !strings.Contains(res.err.Error(), `cannot create from remote "origin/foo"`) {
		t.Fatalf("err = %v; want the early local-branch conflict refusal", res.err)
	}
	if strings.Contains(notes.String(), "creating") {
		t.Errorf("a creation was announced for a plan that will be refused: %q", notes.String())
	}
}

// TestForcedReadFailureSurfacesTheNoteAndKeepsTheCreation checks the whole
// read-failure contract on the -W path: a forced PrimaryOf failure (a real
// git error, not "not a repository") surfaces as the one "wt: note:" line and
// the launch proceeds — with the frozen creation line still announced, from
// ForNew's partial Target, so the person reads what the commit point will
// make. Warn but never prevent.
func TestForcedReadFailureSurfacesTheNoteAndKeepsTheCreation(t *testing.T) {
	root := sharedRootRepo(t)
	plan, err := worktree.Resolve(root, "brand-new")
	if err != nil {
		t.Fatal(err)
	}
	// Force a real git failure at the repository: .git/config becomes a
	// directory and git exits "unknown error occurred while reading the
	// configuration files" — not its "not a git repository" refusal.
	cfg := filepath.Join(root, ".git", "config")
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	tg, readErr := launchwarn.ForNew(root, plan)
	notes := captureNotes(t)
	if err := confirmLaunch(tg, readErr, strings.NewReader(""), osStderr, false); err != nil {
		t.Fatal(err)
	}
	got := notes.String()
	if !strings.Contains(got, "wt: note:") {
		t.Errorf("output = %q, want the read-failure note", got)
	}
	if !strings.Contains(got, "creating branch brand-new and worktree "+plan.Path()) {
		t.Errorf("output = %q, want the frozen creation line from the partial Target", got)
	}
}

// TestTailsRunStopPickerThenSummary pins the non-TUI run tail: the stop picker
// runs, then the summary line — and no removal-offer wording and no gh gather
// run in it (cleanup is user-driven: `wt worktrees` / `wt remove`). This is a
// plain pin of the tail's shape, not a red→green guard (R303a — no RED
// theater). The old removal offer acted only on a terminal, when not kept, and
// with a creation record; the first two are staged here (stdin reports a TTY
// and `[launch] exit_prompt` is not "keep") and the verified mutation supplies
// the third (a creation record), so an offer step re-inserted into the tail
// acts (does not early-return) and its wording / gh gather is caught. Verified
// by mutation:
// re-inserting an offer step into the tail with a creation record that prints
// "Remove it and its branch" / "was created for this session" and runs a
// `gh pr list` gather makes this test fail — the wording and gh assertions
// fire.
func TestTailsRunStopPickerThenSummary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A removal offer acted only on a terminal and when not kept; stage both
	// (the empty Config keeps the offer) so a re-added offer step does not
	// early-return silently.
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	// No gh at exit: the exit-prompt `gh pr list` gather is gone. Fail if any
	// runs in the tail (a re-added removal offer would make exactly this call).
	t.Cleanup(cleanup.StubPRListForTest(func(dir, branch string) ([]byte, error) {
		t.Errorf("gh pr list ran in the run's tail (%s in %s)", branch, dir)
		return []byte("[]"), nil
	}))
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	cases := []struct {
		name string
		run  func() error
		want string // the tail's seam order: no "offer" step
		sum  string // the summary line's stable prefix
	}{
		{"single model", func() error {
			return runAgentCmd(exec.Command(truePath), "claude", config.Model{ID: "ollama/qwen3.8"}, &config.Config{}, nil)
		}, "release,picker,notice", "wt: claude · ollama/qwen3.8 ·"},
		{"slot profile", func() error {
			row := catalog.Row{Model: config.Model{ID: "profile/p"}}
			return runSlotAgentCmd(exec.Command(truePath), "claude", row, &config.Config{}, func() error { return nil })
		}, "release,picker", "wt: claude · profile/p ·"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var order []string
			pr, pp, pn := releaseSession, runStopPicker, emitPriceNotice
			t.Cleanup(func() { releaseSession, runStopPicker, emitPriceNotice = pr, pp, pn })
			releaseSession = func() { order = append(order, "release") }
			runStopPicker = func(*config.Config) { order = append(order, "picker"); fmt.Fprint(os.Stdout, "PICKER-MARKER\n") }
			emitPriceNotice = func(*config.Config) { order = append(order, "notice") }

			var runErr error
			s := captureFile(t, &os.Stdout, func() { runErr = c.run() })
			if runErr != nil {
				t.Fatalf("run: %v", runErr)
			}
			// The tail is release, stop picker, summary (then the notice): no
			// offer step between the picker and the summary.
			if got := strings.Join(order, ","); got != c.want {
				t.Errorf("tail order = %s, want %s (no removal offer)", got, c.want)
			}
			p, sum := strings.Index(s, "PICKER-MARKER"), strings.Index(s, c.sum)
			if p < 0 || sum < 0 || p > sum {
				t.Errorf("stdout = %q, want the stop picker before the summary line", s)
			}
			if strings.Contains(s, "Remove it and its branch") || strings.Contains(s, "was created for this session") {
				t.Errorf("stdout = %q, want no removal offer", s)
			}
		})
	}
}
