package survey

import (
	"bufio"
	"context"
	"errors"
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

// ReleaseSession drops this wt process's own refcount entry, best-effort: a
// failure warns on stderr but never fails the exit path, since the next
// launch's Sweep prunes a dead pid's entry anyway. One implementation for both
// post-exit paths (cmd/wt's runAgentCmd and internal/tui's
// printPendingSummaryAndSurvey), which used to carry identical copies of this
// function and its warning string. Call it BEFORE Picker: the picker offers
// only models no live session uses, so wt's own entry would otherwise always
// count the model this session just used as in use.
func ReleaseSession() {
	if err := refcount.NewStore().Release(os.Getpid()); err != nil {
		fmt.Fprintf(os.Stderr, "note: refcount state not released: %v\n", err)
	}
}

// Picker offers to stop running local models that no live wt session is
// using (issue #115). It prints nothing and returns immediately when stdin is
// not a TTY or when no such model exists. The caller must release wt's own
// refcount entry first, or the model the session just used would always count
// as in use.
func Picker(r io.Reader, w io.Writer, cfg *config.Config) { PickerWith(r, w, cfg, Options{}) }

// Options tunes the stop picker. IncludeInUse lists models a live wt session
// uses (marked with the count) — for explicit `wt stop`; the exit flows leave
// it false.
type Options struct{ IncludeInUse bool }

// PickerWith is Picker with options; same silent-when-not-a-TTY behavior. It
// reports whether the picker had any model to offer (false when stdin is not a
// TTY), so a caller like `wt stop` can say "nothing running" without probing the
// inventory a second time.
func PickerWith(r io.Reader, w io.Writer, cfg *config.Config, opts Options) bool {
	if !stdinTTY() {
		return false
	}
	return runStopPickerWith(r, w, cfg, defaultStopDeps(), opts)
}

// Candidate is one running local model wt can stop, with how many live wt
// sessions use it (for a single-model provider: the whole family's count,
// since stopping any variant stops the provider).
type Candidate struct {
	Entry    localmodels.Entry
	Sessions int
}

// StopCandidates is the exported view `wt stop` uses: every running, trusted,
// stoppable model, in-use ones included.
func StopCandidates(cfg *config.Config) []Candidate {
	return stopCandidates(cfg, defaultStopDeps())
}

func stopCandidates(cfg *config.Config, d stopDeps) []Candidate {
	snap := d.inventory(cfg)
	var cands []localmodels.Entry
	for _, e := range snap.Entries {
		if !e.Running || !lifecycle.CanStop(e.ProviderID) {
			continue
		}
		// A family whose probe failed reports Running it could not confirm —
		// shared with the start/occupant paths so the rule cannot drift.
		if !lifecycle.ProbeTrusted(snap, localmodels.Family(e.ProviderID)) {
			continue
		}
		cands = append(cands, e)
	}
	if len(cands) == 0 {
		return nil
	}
	// Usage is recorded under the registry id a session launched from, but two
	// rows can name one provider-side model (qwen3.8 / qwen3.8:latest). Sum the
	// count over every row that matches this candidate's provider-side name, so
	// a session on either row keeps both off the list.
	aliasOf := func(e localmodels.Entry) []string {
		fam := localmodels.Family(e.ProviderID)
		ids := []string{e.ModelID}
		if e.ModelName == "" {
			return ids
		}
		for _, o := range snap.Entries {
			if o.ModelID == e.ModelID || o.ModelName == "" || localmodels.Family(o.ProviderID) != fam {
				continue
			}
			if lifecycle.SameModel(fam, o.ModelName, e.ModelName) {
				ids = append(ids, o.ModelID)
			}
		}
		return ids
	}
	allIDs := make([]string, 0, len(cands))
	seen := map[string]bool{}
	for _, e := range cands {
		for _, id := range aliasOf(e) {
			if !seen[id] {
				seen[id] = true
				allIDs = append(allIDs, id)
			}
		}
	}
	counts := d.counts(allIDs)
	own := func(e localmodels.Entry) int {
		n := 0
		for _, id := range aliasOf(e) {
			n += counts[id]
		}
		return n
	}
	// A single-model provider (omlx, mtplx) stops as a whole, taking every
	// running variant with it — so its sessions are the whole family's.
	famSessions := map[string]int{}
	for _, e := range cands {
		if lifecycle.SingleModel(e.ProviderID) {
			famSessions[localmodels.Family(e.ProviderID)] += own(e)
		}
	}
	out := make([]Candidate, 0, len(cands))
	for _, e := range cands {
		n := own(e)
		if lifecycle.SingleModel(e.ProviderID) {
			n = famSessions[localmodels.Family(e.ProviderID)]
		}
		out = append(out, Candidate{Entry: e, Sessions: n})
	}
	return out
}

// stopSignalCtx is a test seam: production uses realStopSignalCtx, following the
// var/realfunc shape wt/CLAUDE.md documents for package-level seams. (cmd/wt's
// startSignalCtx covers the same ground for the start path but is an inline
// closure, so what the two share is the intent, not the declaration form.)
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
	runStopPickerWith(r, w, cfg, d, Options{})
}

// runStopPickerWith runs the picker and reports whether it had any model to offer.
func runStopPickerWith(r io.Reader, w io.Writer, cfg *config.Config, d stopDeps, opts Options) bool {
	var offered []localmodels.Entry
	var labels []string
	for _, c := range stopCandidates(cfg, d) {
		if !opts.IncludeInUse && c.Sessions > 0 {
			continue
		}
		label := c.Entry.ModelID
		if c.Sessions > 0 {
			plural := "s"
			if c.Sessions == 1 {
				plural = ""
			}
			label += fmt.Sprintf(" (%d session%s)", c.Sessions, plural)
		}
		offered = append(offered, c.Entry)
		labels = append(labels, label)
	}
	if len(offered) == 0 {
		return false
	}
	header := exitFlowHeader
	if opts.IncludeInUse {
		header = "Stop running local models? (sessions = live wt sessions using it)"
	}
	// Ctrl+C/SIGTERM are caught for the whole picker — the menu read included —
	// so the default disposition cannot kill wt before the summary, the stats
	// and the deferred drain below run. See realStopSignalCtx.
	ctx, cancel := stopSignalCtx()
	defer cancel()
	// The picker read lines the same way the survey did; drop any paste
	// residue so it cannot run as shell commands after wt exits. Deferred so it
	// also runs on the skip paths below: "q"/"esc", or an Enter with nothing
	// ticked, leave just as much residue queued as a confirmed stop does.
	defer flushTTY()
	// The menu read blocks in a syscall a signal does not interrupt (the runtime
	// restarts it), so it runs on its own goroutine and the picker races it
	// against the context. A cancelled menu abandons that goroutine still
	// blocked in Scan: harmless, wt exits shortly after and it never writes to w
	// again (it only prints between reads).
	answer := make(chan []int, 1)
	go func() { answer <- chooseLabeled(bufio.NewScanner(r), w, header, labels) }()
	var selected []int
	select {
	case selected = <-answer:
	case <-ctx.Done():
		fmt.Fprintln(w, "\ncancelled")
		return true
	}
	if len(selected) == 0 {
		return true
	}
	entries := make([]localmodels.Entry, 0, len(selected))
	for _, i := range selected {
		entries = append(entries, offered[i])
	}
	_ = stopEntries(ctx, w, cfg, d, entries)
	return true
}

// stopEntries is the picker's stop loop: sequential stops with progress lines,
// one failure never blocks the rest, Ctrl+C (ctx) cancels the stop in flight
// and skips the remainder. Returns nil, a "N of M stops failed" error, or a
// "cancelled" error.
func stopEntries(ctx context.Context, w io.Writer, cfg *config.Config, d stopDeps, entries []localmodels.Entry) error {
	stoppedFamily := map[string]bool{}
	failed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			// Ctrl+C landed while a stop was in flight (or before the first
			// one): complete the current line and stop offering the rest.
			fmt.Fprintln(w, "cancelled")
			return errors.New("cancelled")
		}
		fmt.Fprintf(w, "Stopping %s... ", e.ModelID)
		fam := localmodels.Family(e.ProviderID)
		// The first stop of a single-model provider already took down the whole
		// provider.
		if lifecycle.SingleModel(e.ProviderID) && stoppedFamily[fam] {
			fmt.Fprintln(w, "done")
			continue
		}
		if err := d.stop(ctx, cfg, e.ProviderID, e.ModelName); err != nil {
			if ctx.Err() != nil {
				// Cancelled mid-stop: the provider CLI was killed, not broken.
				fmt.Fprintln(w, "cancelled")
				return errors.New("cancelled")
			}
			fmt.Fprintf(w, "failed: %v\n", err)
			failed++
			continue
		}
		stoppedFamily[fam] = true
		fmt.Fprintln(w, "done")
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d stops failed", failed, len(entries))
	}
	return nil
}

// StopEntries runs stopEntries under its own Ctrl+C/SIGTERM context.
func StopEntries(w io.Writer, cfg *config.Config, entries []localmodels.Entry) error {
	ctx, cancel := stopSignalCtx()
	defer cancel()
	return stopEntries(ctx, w, cfg, defaultStopDeps(), entries)
}

const exitFlowHeader = "Stop running local models? (none are in use by another wt session)"

// chooseLabeled runs the line-typed checkbox prompt and returns the selected
// indices in list order. Nothing starts selected, so a bare Enter, "q",
// "esc" or an exhausted reader all skip: stopping a model costs a reload, so
// it must be an explicit choice.
func chooseLabeled(sc *bufio.Scanner, w io.Writer, header string, labels []string) []int {
	state := make([]bool, len(labels))
	printChoices(w, header, labels, state)
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
		printChoices(w, header, labels, state)
	}
}

func printChoices(w io.Writer, header string, labels []string, state []bool) {
	fmt.Fprintln(w, header)
	for i, l := range labels {
		mark := " "
		if state[i] {
			mark = "x"
		}
		fmt.Fprintf(w, "  %d [%s] %s\n", i+1, mark, l)
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
