package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
)

// recordCreated records dir as a worktree this launch created, and forgets it
// when the test ends so no other test is prompted.
func recordCreated(t *testing.T, dir string) {
	t.Helper()
	exitprompt.Reset()
	t.Cleanup(exitprompt.Reset)
	if err := exitprompt.Record(dir, "topic"); err != nil {
		t.Fatal(err)
	}
}

// stubExitPromptTails swaps every post-exit seam of the non-TUI tails so a
// test sees the order they run in; the offer and picker write a marker
// through os.Stdout so their position relative to the real summary line is
// pinned too.
func stubExitPromptTails(t *testing.T, order *[]string) {
	t.Helper()
	pr, pp, po, pn := releaseSession, runStopPicker, offerExitPrompt, emitPriceNotice
	t.Cleanup(func() { releaseSession, runStopPicker, offerExitPrompt, emitPriceNotice = pr, pp, po, pn })
	releaseSession = func() { *order = append(*order, "release") }
	runStopPicker = func(*config.Config) { *order = append(*order, "picker"); fmt.Fprint(os.Stdout, "PICKER-MARKER\n") }
	offerExitPrompt = func(*config.Config) { *order = append(*order, "offer"); fmt.Fprint(os.Stdout, "OFFER-MARKER\n") }
	emitPriceNotice = func(*config.Config) { *order = append(*order, "notice") }
}

// TestExitPromptRunsAfterTheStopPickerAndBeforeTheSummary pins the non-TUI
// post-exit order for a single-model launch and a slot-profile launch:
// release, stop picker, worktree prompt, summary. The prompt before the
// picker would ask about a worktree while models are still held; after the
// summary it would scroll away.
func TestExitPromptRunsAfterTheStopPickerAndBeforeTheSummary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	cases := []struct {
		name  string
		run   func() error
		want  string
		after string
	}{
		{"single model", func() error {
			return runAgentCmd(exec.Command(truePath), "claude", config.Model{ID: "ollama/qwen3.8"}, &config.Config{}, nil)
		}, "release,picker,offer,notice", "wt: claude · ollama/qwen3.8 ·"},
		{"slot profile", func() error {
			row := catalog.Row{Model: config.Model{ID: "profile/p"}}
			return runSlotAgentCmd(exec.Command(truePath), "claude", row, &config.Config{}, func() error { return nil })
		}, "release,picker,offer", "wt: claude · profile/p ·"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var order []string
			stubExitPromptTails(t, &order)
			var runErr error
			s := captureFile(t, &os.Stdout, func() { runErr = c.run() })
			if runErr != nil {
				t.Fatalf("run: %v", runErr)
			}
			if got := strings.Join(order, ","); got != c.want {
				t.Errorf("order = %s, want %s", got, c.want)
			}
			p, o, sum := strings.Index(s, "PICKER-MARKER"), strings.Index(s, "OFFER-MARKER"), strings.Index(s, c.after)
			if p < 0 || o < 0 || sum < 0 || !(p < o && o < sum) {
				t.Errorf("stdout = %q, want picker, then offer, then the summary line", s)
			}
		})
	}
}

// TestCommandAgentTailsOfferToo pins that a shell launch (no model, so no
// stop picker) is offered the created worktree like a model launch: the
// worktree is the thing the prompt is about, not the model.
func TestCommandAgentTailsOfferToo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	var order []string
	stubExitPromptTails(t, &order)
	captureFile(t, &os.Stdout, func() {
		_ = runAgentCmd(exec.Command(truePath), "shell", config.Model{}, &config.Config{}, nil)
	})
	if got := strings.Join(order, ","); got != "release,offer" {
		t.Errorf("order = %s, want release,offer", got)
	}
}

// TestExitPromptNeverChangesTheAgentExitStatus runs a launch whose agent
// exits 3 in a child process (runAgentCmd ends in os.Exit) with the prompt
// stubbed to print and return: wt must still exit 3 after the whole tail, so
// a script reading the status never sees the prompt's outcome.
func TestExitPromptNeverChangesTheAgentExitStatus(t *testing.T) {
	if os.Getenv("WT_TEST_EXIT_PROMPT_CHILD") == "1" {
		offerExitPrompt = func(*config.Config) { fmt.Println("OFFERED") }
		runStopPicker = func(*config.Config) {}
		_ = runAgentCmd(exec.Command("sh", "-c", "exit 3"), "shell", config.Model{}, &config.Config{}, nil)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestExitPromptNeverChangesTheAgentExitStatus$")
	cmd.Env = append(os.Environ(), "WT_TEST_EXIT_PROMPT_CHILD=1")
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("child err = %v, want exit status 3\n%s", err, out)
	}
	if !strings.Contains(string(out), "OFFERED") {
		t.Errorf("child output = %q, want the prompt to have run before the exit", out)
	}
}

// TestExitPromptSkippedWithoutATerminalOrWhenKept runs the real offer seam
// with a worktree recorded: no terminal, or exit_prompt = "keep", prints
// nothing and reads nothing, so a script or a user who opted out is never
// asked.
func TestExitPromptSkippedWithoutATerminalOrWhenKept(t *testing.T) {
	cases := []struct {
		name string
		tty  bool
		cfg  *config.Config
	}{
		{"no terminal", false, &config.Config{}},
		{"keep", true, &config.Config{LaunchTable: &config.LaunchState{ExitPrompt: "keep"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := initTestRepo(t)
			t.Chdir(dir)
			recordCreated(t, dir)
			old := stdinTTY
			stdinTTY = func() bool { return c.tty }
			t.Cleanup(func() { stdinTTY = old })
			s := captureFile(t, &os.Stdout, func() { realOfferExitPrompt(c.cfg) })
			if s != "" {
				t.Errorf("stdout = %q, want nothing", s)
			}
		})
	}
}
