package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// slotParityFile is the scenario table both launch paths are held to. The
// picker's test (internal/tui, TestSlotLaunchParity) reads the same file.
const slotParityFile = "../../internal/profiles/testdata/slot_launch_parity.txt"

// slotParityScenario is one block of slotParityFile.
type slotParityScenario struct {
	name, local, writes string
	slots               map[string]string
	steps               []string
}

// readSlotParityScenarios parses slotParityFile: blocks of "key: value"
// lines separated by blank lines, "#" lines ignored. The picker's test keeps
// a copy of this parser; the file, not the parser, is what is shared.
func readSlotParityScenarios(t *testing.T) []slotParityScenario {
	t.Helper()
	data, err := os.ReadFile(slotParityFile)
	if err != nil {
		t.Fatalf("read %s: %v", slotParityFile, err)
	}
	var out []slotParityScenario
	var cur *slotParityScenario
	flush := func() {
		if cur != nil {
			if cur.name == "" || len(cur.steps) == 0 || len(cur.slots) == 0 {
				t.Fatalf("%s: incomplete scenario %+v", slotParityFile, *cur)
			}
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if line == "" {
			flush()
			continue
		}
		key, val, ok := strings.Cut(line, ": ")
		if !ok {
			t.Fatalf("%s: malformed line %q", slotParityFile, line)
		}
		if cur == nil {
			cur = &slotParityScenario{slots: map[string]string{}}
		}
		switch key {
		case "scenario":
			cur.name = val
		case "slots":
			for _, pair := range strings.Fields(val) {
				slot, model, _ := strings.Cut(pair, "=")
				cur.slots[slot] = model
			}
		case "local":
			cur.local = val
		case "writes":
			cur.writes = val
		case "steps":
			cur.steps = strings.Fields(val)
		default:
			t.Fatalf("%s: unknown key %q", slotParityFile, key)
		}
	}
	flush()
	if len(out) == 0 {
		t.Fatalf("%s: no scenarios", slotParityFile)
	}
	return out
}

// slotParityArg is the profile arg each parity scenario carries. The applier
// appends it to the agent's argv, so the agent seeing it is how the run
// observes that the applier ran.
const slotParityArg = "--wt-parity-applied"

// slotParityAgent is the command a parity launch runs in place of the agent:
// it appends to logPath "apply" when its argv carries slotParityArg, "record"
// when statePath (last-picked.state) already exists, then "run".
func slotParityAgent(logPath, statePath string) *exec.Cmd {
	script := `log=$1; state=$2; shift 2
for a; do [ "$a" = "` + slotParityArg + `" ] && echo apply >>"$log"; done
[ -f "$state" ] && echo record >>"$log"
echo run >>"$log"`
	return exec.Command("sh", "-c", script, "sh", logPath, statePath)
}

// TestSlotLaunchParity runs every scenario of the shared table
// (internal/profiles/testdata/slot_launch_parity.txt) through the -M
// profile/<name> pin, launchSlotProfile, and checks the seam calls it makes
// against the table's steps. internal/tui's TestSlotLaunchParity runs the
// same table through the picker (prepareSlot, handleSlotMsg,
// settleSlotAfterStart, launchSlot), so the two paths are held to one list:
// docs/internals/launch-flow.md promises they run the same ten steps, and a
// change to one path's order (a restart skipped, a probe before the wait, an
// owed restart not handed to the start) would otherwise pass that path's own
// hand-written test and leave the paths launching Claude Code differently.
//
// What the normalization leaves out, on purpose:
//   - the replace permission handed to the start: the pin passes the
//     process-wide --replace, the picker passes it only for the pinned row
//     (TestLaunchSlotProfilePassesReplaceToTheStart pins this path's);
//   - what is printed: the pin's "restarting the LiteLLM proxy" line on
//     stderr, the picker's notes buffer and screens;
//   - the local model's id, which is each package fixture's own (LOCAL);
//   - how the start runs: here the startSlotModel seam stands for the whole
//     start, in the picker the start flow drives the engine seam.
//
// apply and record are seen from the run, by slotParityAgent, since neither is
// a seam on this path: both happened before the agent ran.
func TestSlotLaunchParity(t *testing.T) {
	const localID = "omlx/q"
	for _, sc := range readSlotParityScenarios(t) {
		t.Run(sc.name, func(t *testing.T) {
			stateDir, worktree := slotLaunchEnv(t)
			// Otherwise TestMain's inventory: omlx/q on disk and not running.
			if sc.local == "running" {
				stubProbeInventory(t, runningQ())
			}
			slots := map[string]string{}
			for slot, model := range sc.slots {
				if model == "LOCAL" {
					model = localID
				}
				slots[slot] = model
			}
			check := slotFixtureCheck(0, "p", "opus", slots)
			check.Profile.Args = []string{slotParityArg}
			stubSlotChecks(t, check)

			// stubSlotSeams saves and restores every seam; the recorders
			// below replace its stubs for this test.
			stubSlotSeams(t)
			var mu sync.Mutex
			var log []string
			rec := func(e string) { mu.Lock(); log = append(log, e); mu.Unlock() }
			norm := func(id string) string {
				if id == localID {
					return "LOCAL"
				}
				return id
			}
			ensureSlotRoutes = func(_ context.Context, _ io.Writer, _ *config.Config, models []config.Model, owedIn bool) (bool, error) {
				ids := make([]string, len(models))
				role := "cloud"
				for i, m := range models {
					ids[i] = norm(m.ID)
					if m.ID == localID {
						role = "local"
					}
				}
				rec(fmt.Sprintf("ensure:%s:owed=%t", strings.Join(ids, ","), owedIn))
				return owedIn || sc.writes == role, nil
			}
			startSlotModel = func(_ *config.Config, row catalog.Row, _ bool, owed bool) (bool, error) {
				rec(fmt.Sprintf("start:%s:owed=%t", norm(row.Model.ID), owed))
				return false, nil
			}
			restartProxyNow = func(context.Context, *config.Config) error { rec("restart"); return nil }
			waitPendingRoutes = func() { rec("wait") }
			probeProxyNow = func(context.Context, *config.Config) error { rec("probe"); return nil }
			settleRoutesNow = func(context.Context, *config.Config) { rec("settle") }
			runLog := filepath.Join(t.TempDir(), "run.log")
			buildSlotCmd = func(_ profiles.SlotLaunch, worktreePath string, _ bool, _ *config.Config, _ []string) (*exec.Cmd, error) {
				rec("build")
				cmd := slotParityAgent(runLog, filepath.Join(stateDir, "last-picked.state"))
				cmd.Dir = worktreePath
				return cmd, nil
			}

			var err error
			captureFile(t, &os.Stdout, func() {
				err = pinSlotProfile(worktree, slotFixtureConfig(), "profile/p")
			})
			if err != nil {
				t.Fatalf("launch error = %v", err)
			}
			ran, rerr := os.ReadFile(runLog)
			if rerr != nil {
				t.Fatalf("the agent command did not run: %v", rerr)
			}
			got := append(log, strings.Fields(string(ran))...)
			if g, w := strings.Join(got, " "), strings.Join(sc.steps, " "); g != w {
				t.Errorf("steps = %s\n want  %s", g, w)
			}
		})
	}
}
