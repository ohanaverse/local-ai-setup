package survey

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
)

// stopDeps are the picker's seams: live inventory, the two live-session
// refcount reads, the per-model stop, the batch's single proxy restart and the
// pidfile read behind a refused port. Tests substitute all six.
type stopDeps struct {
	inventory func(*config.Config) localmodels.Snapshot
	counts    func(modelIDs []string) map[string]int
	// live is the session count of every model id with a live session,
	// unregistered ids included (refcount.Live) — the family-wide count.
	live func() map[string]int
	// stop stops one model and writes its route removal without restarting
	// the proxy; restartOwed reports that settle must run (issue #142).
	stop   func(ctx context.Context, cfg *config.Config, en localmodels.Entry) (restartOwed bool, err error)
	settle func(ctx context.Context, cfg *config.Config)
	// loading reads what the pidfile of a family's server says, for a family
	// whose port refused (lifecycle.LoadingServer). It signals nothing.
	loading func(cfg *config.Config, family string) lifecycle.Loading
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
		live: func() map[string]int {
			_ = store.Sweep()
			return store.Live()
		},
		stop:    lifecycle.StopModelDeferred,
		settle:  lifecycle.SettleRoutes,
		loading: lifecycle.LoadingServer,
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
// TTY), the provider families whose probe gave no usable answer
// (StopState.Unknown) and what the pidfiles behind refused ports said
// (StopState.Loading), so a caller like `wt stop` can say "nothing running"
// only when that is known, without probing the inventory a second time.
func PickerWith(r io.Reader, w io.Writer, cfg *config.Config, opts Options) (offered bool, unknown []string, loading map[string]lifecycle.Loading) {
	if !stdinTTY() {
		return false, nil, nil
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

// FamilyState is what one inventory round knows about a stoppable provider
// family as a whole, whether or not it produced a candidate. The zero value
// is a family that was not probed: nothing is known about its server.
type FamilyState struct {
	// Probed: the inventory probed the family, so Untrusted and Down are an
	// answer. False for a family the snapshot carries no status for — a
	// provider row the inventory does not probe (no location and no local
	// model), or a family known only from a live session's model id — whose
	// Untrusted and Down are false because nobody asked.
	Probed bool
	// Untrusted: the probe could not say what the family is running
	// (!lifecycle.ProbeTrusted), so it has no candidates whatever it serves.
	Untrusted bool
	// Down: the server actively refused the connection (Snapshot.Down) —
	// nothing is listening, which is known even when Untrusted.
	Down bool
	// Sessions is the number of live wt sessions on any model of the family,
	// and Users the model ids they use, sorted. Taken from every live
	// refcount entry, so it does not depend on the candidates: it counts a
	// model that does not read as running and a discovered model no registry
	// lists. A stop that takes the whole provider down asks about these.
	Sessions int
	Users    []string
	// Loading is what the family's pidfile says when Down: a server that has
	// not opened its port yet (Loading.PID, verified — mtplx reads its
	// weights first), a live process wt could not verify as one
	// (Loading.Stray), or a process table it could not read (Loading.Err).
	// Zero when the port did not refuse, and for a family with no pidfile.
	Loading lifecycle.Loading
}

// StopState is the view `wt stop` acts on, from one inventory round:
// Candidates is every running, trusted, stoppable model, in-use ones
// included; Families is keyed by provider family (localmodels.Family).
type StopState struct {
	Candidates []Candidate
	Families   map[string]FamilyState
}

// Unknown lists, sorted, the families whose probe was neither trusted nor
// refused: wt cannot say whether they are running anything.
func (s StopState) Unknown() []string {
	var out []string
	for fam, fs := range s.Families {
		if fs.Untrusted && !fs.Down {
			out = append(out, fam)
		}
	}
	sort.Strings(out)
	return out
}

// Loading returns, by family, what a pidfile behind a refused port said
// (FamilyState.Loading) for the families where it said anything: a server
// still loading, a live process that is not one, or a process table wt could
// not read. None of them is a candidate, so the picker cannot list them.
func (s StopState) Loading() map[string]lifecycle.Loading {
	out := map[string]lifecycle.Loading{}
	for fam, fs := range s.Families {
		if fs.Loading != (lifecycle.Loading{}) {
			out[fam] = fs.Loading
		}
	}
	return out
}

// ReadStopState takes one live inventory snapshot and one read of the live
// sessions and returns what `wt stop` needs from them.
func ReadStopState(cfg *config.Config) StopState {
	return stopState(cfg, defaultStopDeps())
}

func stopState(cfg *config.Config, d stopDeps) StopState {
	snap := d.inventory(cfg)
	st := StopState{Candidates: candidatesFrom(snap, d), Families: map[string]FamilyState{}}
	for fam := range snap.Providers {
		if lifecycle.CanStop(fam) {
			fs := FamilyState{Probed: true, Untrusted: !lifecycle.ProbeTrusted(snap, fam), Down: snap.Down[fam]}
			if fs.Down {
				// Nothing listens, which is not yet "nothing is there".
				fs.Loading = d.loading(cfg, fam)
			}
			st.Families[fam] = fs
		}
	}
	for id, n := range d.live() {
		fam := modelFamily(cfg, id)
		if n == 0 || !lifecycle.CanStop(fam) {
			continue
		}
		fs := st.Families[fam]
		fs.Sessions += n
		fs.Users = append(fs.Users, id)
		st.Families[fam] = fs
	}
	for fam, fs := range st.Families {
		sort.Strings(fs.Users)
		st.Families[fam] = fs
	}
	return st
}

// modelFamily is the provider family a session's model id belongs to: the
// registry model's provider when the id is registered (a cloud model has no
// local server, so none), else the id's own prefix — a discovered model's id
// is family-prefixed (config.DiscoveredModelID). "" when wt has no probe for
// it.
func modelFamily(cfg *config.Config, id string) string {
	if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
		m := cfg.Models[i]
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			return ""
		}
		return localmodels.Family(m.ProviderID)
	}
	prefix, _, _ := strings.Cut(id, "/")
	return localmodels.Family(prefix)
}

func candidatesFrom(snap localmodels.Snapshot, d stopDeps) []Candidate {
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
	// An Exclusive provider (mtplx) stops as a whole, taking every running
	// variant with it — so its sessions are the whole family's. A Pool (omlx)
	// unloads one model at a time, so each model keeps its own count.
	famSessions := map[string]int{}
	for _, e := range cands {
		if lifecycle.TenancyOf(e.ProviderID) == lifecycle.Exclusive {
			famSessions[localmodels.Family(e.ProviderID)] += own(e)
		}
	}
	out := make([]Candidate, 0, len(cands))
	for _, e := range cands {
		n := own(e)
		if lifecycle.TenancyOf(e.ProviderID) == lifecycle.Exclusive {
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

// runStopPickerWith runs the picker and reports whether it had any model to
// offer, the families it could not read (StopState.Unknown) and what it
// cannot list because no port answers for it (StopState.Loading).
func runStopPickerWith(r io.Reader, w io.Writer, cfg *config.Config, d stopDeps, opts Options) (bool, []string, map[string]lifecycle.Loading) {
	var offered []localmodels.Entry
	var labels []string
	st := stopState(cfg, d)
	unknown, loading := st.Unknown(), st.Loading()
	for _, c := range st.Candidates {
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
		return false, unknown, loading
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
	// also runs on the skip paths below: "q"/"esc", or a bare Enter, leave just
	// as much residue queued as a confirmed stop does.
	defer flushTTY()
	// The reader goroutine below and this one both write to w: it prints the
	// prompt, and a signal landing before the read is answered prints
	// "cancelled" here. One serialized writer keeps their lines whole —
	// unserialized, a signal arriving mid-prompt interleaves the two on a
	// terminal and races outright when w is a test's bytes.Buffer.
	w = &lockedWriter{w: w}
	// The menu read blocks in a syscall a signal does not interrupt (the runtime
	// restarts it), so it runs on its own goroutine and the picker races it
	// against the context. A cancelled menu abandons that goroutine still
	// blocked in Scan: harmless, wt exits shortly after. It is still printing
	// the prompt until it gets there, which is the race lockedWriter covers.
	answer := make(chan []int, 1)
	go func() { answer <- chooseLabeled(bufio.NewScanner(r), w, header, labels) }()
	var selected []int
	select {
	case selected = <-answer:
	case <-ctx.Done():
		fmt.Fprintln(w, "\ncancelled")
		return true, unknown, loading
	}
	if len(selected) == 0 {
		return true, unknown, loading
	}
	entries := make([]localmodels.Entry, 0, len(selected))
	for _, i := range selected {
		entries = append(entries, offered[i])
	}
	_ = stopEntries(ctx, w, cfg, d, entries)
	return true, unknown, loading
}

// ErrStopCancelled is what a stop loop returns when Ctrl+C or SIGTERM ended it
// early. It is a value, not only a message, so a caller with more to stop
// after the loop (`wt stop --all`, which halts the omlx service next) can tell
// "the user asked to stop" from "a stop failed" and leave the rest alone.
var ErrStopCancelled = errors.New("cancelled")

// stopEntries is the picker's stop loop: sequential stops with progress lines,
// one failure never blocks the rest, Ctrl+C (ctx) cancels the stop in flight
// and skips the remainder. Returns nil, a "N of M stops failed" error, or
// ErrStopCancelled.
//
// The stops only write their route removals; one settle from the deferred tail
// of the loop restarts the LiteLLM proxy once for the whole batch (issue #142).
// The defer runs on every exit path, cancel and failure included, but calls the
// settle only when some stop owed a restart: removals already written still
// need that restart to take effect, and a batch that wrote nothing must not
// bounce the proxy at all.
func stopEntries(ctx context.Context, w io.Writer, cfg *config.Config, d stopDeps, entries []localmodels.Entry) error {
	stoppedFamily := map[string]bool{}
	failed := 0
	restartOwed := false
	defer func() {
		if restartOwed {
			d.settle(ctx, cfg)
		}
	}()
	for _, e := range entries {
		if ctx.Err() != nil {
			// Ctrl+C landed while a stop was in flight (or before the first
			// one): complete the current line and stop offering the rest.
			fmt.Fprintln(w, "cancelled")
			return ErrStopCancelled
		}
		fmt.Fprintf(w, "Stopping %s... ", e.ModelID)
		fam := localmodels.Family(e.ProviderID)
		// The first stop of a single-model provider already took down the whole
		// provider.
		if lifecycle.TenancyOf(e.ProviderID) == lifecycle.Exclusive && stoppedFamily[fam] {
			fmt.Fprintln(w, "done")
			continue
		}
		// The whole entry goes through: its ModelID is the route id the
		// removal must use, so an omlx model whose name merely ends in a
		// sibling's cannot have the sibling's route removed instead (#195).
		owed, err := d.stop(ctx, cfg, e)
		restartOwed = restartOwed || owed
		if err != nil {
			if ctx.Err() != nil {
				// Cancelled mid-stop: the provider CLI was killed, not broken.
				fmt.Fprintln(w, "cancelled")
				return ErrStopCancelled
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
//
// The batch's settle starts the proxy restart asynchronously (issue #142), so
// the caller owes a lifecycle.WaitPendingRoutes() before the process exits — a
// goroutine does not survive main returning, and a restart killed mid-flight
// leaves the proxy serving the removed routes — or before it hands the proxy to
// anything else. Its one caller, `wt stop`, reaches the wait from main's exit
// path; the picker's own call to stopEntries exits the same way.
func StopEntries(w io.Writer, cfg *config.Config, entries []localmodels.Entry) error {
	ctx, cancel := stopSignalCtx()
	defer cancel()
	return stopEntries(ctx, w, cfg, defaultStopDeps(), entries)
}

const exitFlowHeader = "Stop running local models? (none are in use by another wt session)"

// lockedWriter serializes writes to one writer from the picker's two
// goroutines: the reader printing the prompt (the header and list, then one
// line per rejected answer) and the picker printing "cancelled" when a signal
// wins the race. It keeps each write whole rather than ordering the two, which
// is all a terminal needs and is what makes a shared bytes.Buffer race-free.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

// chooseLabeled runs the one-line prompt and returns the chosen indices in
// list order (#139). Enter applies what was typed, with no confirming step:
//
//   - nothing (or "q", "esc", "skip", or an exhausted reader) stops nothing —
//     the default, because stopping a model costs a reload and must be an
//     explicit choice;
//   - "a" or "all" chooses every model;
//   - space-separated numbers choose those models, each once.
//
// Anything else rejects the whole line — "1 x" does not stop model 1 on a
// typo — says which part was not understood, and asks again. It is not more
// elaborate than that on purpose: whatever this prompt leaves running, `wt
// stop` stops.
func chooseLabeled(sc *bufio.Scanner, w io.Writer, header string, labels []string) []int {
	fmt.Fprintln(w, header)
	for i, l := range labels {
		fmt.Fprintf(w, "  %d  %s\n", i+1, l)
	}
	fmt.Fprintln(w, "  numbers (e.g. 1 2) · a = all · q = skip · Enter = none")
	for {
		fmt.Fprint(w, "stop> ")
		if !sc.Scan() {
			return nil
		}
		line := strings.ToLower(strings.TrimSpace(sc.Text()))
		switch line {
		case "", "q", "esc", "skip":
			return nil
		case "a", "all":
			all := make([]int, len(labels))
			for i := range all {
				all[i] = i
			}
			return all
		}
		chosen := make([]bool, len(labels))
		var bad []string
		for _, f := range strings.Fields(line) {
			if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(labels) {
				chosen[n-1] = true
				continue
			}
			bad = append(bad, strconv.Quote(f))
		}
		if len(bad) > 0 {
			fmt.Fprintf(w, "not a number from the list: %s — type numbers (e.g. 1 2), a for all, or Enter to stop nothing\n", strings.Join(bad, " "))
			continue
		}
		var out []int
		for i, on := range chosen {
			if on {
				out = append(out, i)
			}
		}
		return out
	}
}
