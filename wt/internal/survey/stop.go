package survey

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
)

// stopDeps are the picker's seams: live inventory, the live-session refcount
// read, and the per-model stop. Tests substitute all three.
type stopDeps struct {
	inventory func(*config.Config) localmodels.Snapshot
	counts    func(modelIDs []string) map[string]int
	stop      func(ctx context.Context, cfg *config.Config, providerID, modelName string) error
}

func defaultStopDeps() stopDeps {
	store := refcount.NewStore()
	return stopDeps{
		inventory: localmodels.Inventory,
		// Sweep first: Sweep otherwise runs only at launch start, so an entry
		// left by a killed session would count as "in use" forever.
		counts: func(ids []string) map[string]int {
			_ = store.Sweep()
			return store.Counts(ids)
		},
		stop: lifecycle.StopModel,
	}
}

// Picker offers to stop running local models that no live wt session is
// using (issue #115). It prints nothing and returns immediately when stdin is
// not a TTY or when no such model exists. The caller must release wt's own
// refcount entry first, or the model the session just used would always count
// as in use.
func Picker(r io.Reader, w io.Writer, cfg *config.Config) {
	if !stdinTTY() {
		return
	}
	runStopPicker(r, w, cfg, defaultStopDeps())
}

// stoppable lists the running local models the picker may offer: probed
// trustworthily, with a stop backend, and with zero live sessions using them.
func stoppable(cfg *config.Config, d stopDeps) []localmodels.Entry {
	snap := d.inventory(cfg)
	var cands []localmodels.Entry
	for _, e := range snap.Entries {
		if !e.Running || !lifecycle.CanStop(e.ProviderID) {
			continue
		}
		// A family whose probe failed reports Running it could not confirm.
		if st, ok := snap.Providers[localmodels.Family(e.ProviderID)]; ok && st != localmodels.StatusOK {
			continue
		}
		cands = append(cands, e)
	}
	if len(cands) == 0 {
		return nil
	}
	ids := make([]string, len(cands))
	for i, e := range cands {
		ids[i] = e.ModelID
	}
	counts := d.counts(ids)
	// A single-model provider (omlx, mtplx) stops as a whole, taking every
	// running variant with it — so one session using any variant keeps the
	// whole family off the list, not just its own row.
	familyBusy := map[string]bool{}
	for _, e := range cands {
		if counts[e.ModelID] > 0 && lifecycle.SingleModel(e.ProviderID) {
			familyBusy[localmodels.Family(e.ProviderID)] = true
		}
	}
	var out []localmodels.Entry
	for _, e := range cands {
		if counts[e.ModelID] == 0 && !familyBusy[localmodels.Family(e.ProviderID)] {
			out = append(out, e)
		}
	}
	return out
}

// stopSignalCtx is a test seam: production uses realStopSignalCtx. It mirrors
// cmd/wt/start.go's startSignalCtx, in the var/realfunc shape wt/CLAUDE.md
// documents for new seams.
var stopSignalCtx = realStopSignalCtx

// realStopSignalCtx returns a context cancelled by Ctrl+C or SIGTERM. The
// picker's stops run after the agent has exited, so nothing above it is
// watching for those signals any more: without a context of its own, a wedged
// `ollama stop`/`omlx stop`/`mtplx stop` would block a session that has already
// finished its agent, with no way out but killing the terminal. Cancelling is
// deliberately not fatal — the stop in flight is killed, the rest are skipped,
// and wt still prints its summary.
func realStopSignalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runStopPicker(r io.Reader, w io.Writer, cfg *config.Config, d stopDeps) {
	offered := stoppable(cfg, d)
	if len(offered) == 0 {
		return
	}
	selected := chooseModels(bufio.NewScanner(r), w, offered)
	// The picker read lines the same way the survey did; drop any paste
	// residue so it cannot run as shell commands after wt exits. Deferred so it
	// also runs on the skip paths below: "q"/"esc", or an Enter with nothing
	// ticked, leave just as much residue queued as a confirmed stop does, and
	// the early return used to skip the drain entirely.
	defer flushTTY()
	if len(selected) == 0 {
		return
	}
	// These stops run after the agent has exited, so this path owns the only
	// Ctrl+C handling left — see realStopSignalCtx.
	ctx, cancel := stopSignalCtx()
	defer cancel()
	stoppedFamily := map[string]bool{}
	for _, i := range selected {
		if ctx.Err() != nil {
			// Ctrl+C landed while a stop was in flight (or before the first
			// one): complete the current line and stop offering the rest
			// rather than starting stops nobody is waiting for.
			fmt.Fprintln(w, "cancelled")
			break
		}
		e := offered[i]
		fmt.Fprintf(w, "Stopping %s... ", e.ModelID)
		fam := localmodels.Family(e.ProviderID)
		if lifecycle.SingleModel(e.ProviderID) {
			// The first stop already took down the whole provider.
			if stoppedFamily[fam] {
				fmt.Fprintln(w, "done")
				continue
			}
		}
		if err := d.stop(ctx, cfg, e.ProviderID, e.ModelName); err != nil {
			fmt.Fprintf(w, "failed: %v\n", err)
			continue
		}
		stoppedFamily[fam] = true
		fmt.Fprintln(w, "done")
	}
}

// chooseModels runs the line-typed checkbox prompt and returns the selected
// indices in list order. Nothing starts selected, so a bare Enter, "q",
// "esc" or an exhausted reader all skip: stopping a model costs a reload, so
// it must be an explicit choice.
func chooseModels(sc *bufio.Scanner, w io.Writer, offered []localmodels.Entry) []int {
	state := make([]bool, len(offered))
	printChoices(w, offered, state)
	for {
		fmt.Fprint(w, "stop> ")
		if !sc.Scan() {
			return nil
		}
		line := strings.ToLower(strings.TrimSpace(sc.Text()))
		switch line {
		case "":
			return selectedIndices(state)
		case "q", "esc", "skip":
			return nil
		case "all", "a":
			for i := range state {
				state[i] = true
			}
		case "none", "n":
			for i := range state {
				state[i] = false
			}
		default:
			toggled := false
			for _, f := range strings.Fields(line) {
				if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(state) {
					state[n-1] = !state[n-1]
					toggled = true
				}
			}
			if !toggled {
				fmt.Fprintln(w, "type a number to toggle, all, none, Enter to confirm, or q to skip")
				continue
			}
		}
		printChoices(w, offered, state)
	}
}

func printChoices(w io.Writer, offered []localmodels.Entry, state []bool) {
	fmt.Fprintln(w, "Stop running local models? (none are in use by another wt session)")
	for i, e := range offered {
		mark := " "
		if state[i] {
			mark = "x"
		}
		fmt.Fprintf(w, "  %d [%s] %s\n", i+1, mark, e.ModelID)
	}
	fmt.Fprintln(w, "  number toggles · all · none · Enter confirms · q skips")
}

func selectedIndices(state []bool) []int {
	var out []int
	for i, on := range state {
		if on {
			out = append(out, i)
		}
	}
	return out
}
