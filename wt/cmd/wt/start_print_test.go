package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// failedStartMessage is a failed start as the engine words it for mtplx: one
// line, then the server's log tail on lines of its own.
const failedStartMessage = "failed to start mtplx/m: serve process exited during model load: exit status 1; log tail: loading weights\nfatal: out of memory"

// stubFailedStart makes the start driver fail as startForLaunch does, with
// failedStartMessage.
func stubFailedStart(t *testing.T) {
	t.Helper()
	old := startModel
	startModel = func(*config.Config, catalog.Row, bool) error {
		return startFailure("mtplx/m", errors.New(strings.TrimPrefix(failedStartMessage, "failed to start mtplx/m: ")))
	}
	t.Cleanup(func() { startModel = old })
}

// TestStartCmdLeavesAFailedStartToMainsOnePrint pins #344 for `wt start`:
// main prints every error once, after "wt:", and cobra printed it too, as
// "Error: …", so a failed start came out twice with the server's multi-line
// log tail each time. The command must hand the error back (main's line, and
// exit 1) and print nothing of it itself. `--replace` and the picker are the
// same command and the same return.
func TestStartCmdLeavesAFailedStartToMainsOnePrint(t *testing.T) {
	for name, args := range map[string][]string{
		"wt start <id>":           {"ollama/b:1"},
		"wt start <id> --replace": {"ollama/b:1", "--replace"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := startFixture(t)
			stubFailedStart(t)
			c := startCmd(&app{cfg: cfg})
			c.Flags().Bool("replace", false, "") // the root command's persistent flag
			var out, errOut bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&errOut)
			c.SetArgs(args)
			err := c.Execute()
			if err == nil || err.Error() != failedStartMessage {
				t.Fatalf("err = %v, want the failed start handed back for main to print", err)
			}
			if out.Len() != 0 || errOut.Len() != 0 {
				t.Errorf("the command printed stdout %q, stderr %q; want nothing: main prints the error once", out.String(), errOut.String())
			}
		})
	}
}

// TestSmokeCmdLeavesAFailedStartToMainsOnePrint is the same for `wt smoke`,
// which starts an idle model before its rows: the failure is returned, and
// cobra prints no "Error:" copy of it — and no usage block either, which with
// the "Error:" line gone would stand alone above main's line, for a failure
// that is not a usage mistake.
func TestSmokeCmdLeavesAFailedStartToMainsOnePrint(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubFailedStart(t)
	oldRel, oldPick, oldTTY := releaseSession, runStopPicker, stdinTTY
	t.Cleanup(func() { releaseSession, runStopPicker, stdinTTY = oldRel, oldPick, oldTTY })
	releaseSession = func() {}
	runStopPicker = func(*config.Config) {}
	stdinTTY = func() bool { return true }

	c := smokeCmd(&app{cfg: cfg})
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	c.SetArgs([]string{"ollama/not-eligible:x"})
	err := c.Execute()
	if err == nil || err.Error() != failedStartMessage {
		t.Fatalf("err = %v, want the failed start handed back for main to print", err)
	}
	if strings.Contains(out.String()+errOut.String(), "failed to start") {
		t.Errorf("the command printed the error itself (stdout %q, stderr %q); main prints it once", out.String(), errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "Usage:") {
		t.Errorf("the command printed its usage after a failed start (stdout %q, stderr %q)", out.String(), errOut.String())
	}
}

// TestLaunchLeavesAFailedStartToMainsOnePrint is the same for a launch whose
// -M pin has to be started. The root command keeps cobra's print for its
// other errors, so it is silenced for this run only, and only by an error
// that came out of the start: the driver's own (*startError) or anything
// else the start returned — a refused replace, a cancel — which resolveModel
// marks. The usage block the root command prints after an error is silenced
// with it. It runs the real launch funnel (runLaunchPath) on the root command.
func TestLaunchLeavesAFailedStartToMainsOnePrint(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true}},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	refused := errors.New("starting omlx/q will stop omlx/old; continue? — rerun with --replace to confirm")
	for name, tc := range map[string]struct {
		pin     string
		start   error
		silence bool
	}{
		"the driver's failure":      {"omlx/q", startFailure("omlx/q", errors.New("boom")), true},
		"a refused replace":         {"omlx/q", refused, true},
		"an error that is no start": {"omlx/nope", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			stubStartDriver(t, tc.start)
			root := rootCmd()
			err := runLaunchPath(root, &app{cfg: cfg}, "pi", tc.pin, "", "", nil, t.TempDir(), "")
			if err == nil {
				t.Fatal("runLaunchPath() = nil, want an error")
			}
			if tc.start != nil && (!errors.Is(err, tc.start) || err.Error() != tc.start.Error()) {
				t.Errorf("err = %v, want the start's error, text unchanged", err)
			}
			if root.SilenceErrors != tc.silence || root.SilenceUsage != tc.silence {
				t.Errorf("SilenceErrors = %v, SilenceUsage = %v, want both %v", root.SilenceErrors, root.SilenceUsage, tc.silence)
			}
		})
	}
}

// TestPrintOnceSilencesCobraForAStartErrorOnly runs the mark through cobra
// itself, on a command set up as the root command is (no SilenceUsage): for
// an error printOnce recognises Execute prints nothing — not the error, and
// not the usage block, which would otherwise stand alone between the start's
// progress and main's line — and any other error is still printed as
// "Error: …" with the usage after it. It is what ties the fields the launch
// test reads to what the user sees.
func TestPrintOnceSilencesCobraForAStartErrorOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		silent bool
	}{
		"a start error":   {asStartError(errors.New("boom")), true},
		"any other error": {errors.New("boom"), false},
	} {
		t.Run(name, func(t *testing.T) {
			c := &cobra.Command{Use: "x"}
			c.RunE = func(cmd *cobra.Command, _ []string) error { return printOnce(cmd, tc.err) }
			var out, errOut bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&errOut)
			c.SetArgs(nil)
			if err := c.Execute(); err == nil || err.Error() != "boom" {
				t.Fatalf("err = %v, want boom", err)
			}
			printed := errOut.String() + out.String()
			if tc.silent && printed != "" {
				t.Errorf("cobra printed %q, want nothing", printed)
			}
			if !tc.silent && (!strings.HasPrefix(printed, "Error: boom\n") || !strings.Contains(printed, "Usage:")) {
				t.Errorf("cobra printed %q, want the error and the usage, as before", printed)
			}
		})
	}
}
