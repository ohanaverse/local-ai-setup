// Package hoststop is the host half of `wt stop`: one live inventory round
// read into the stoppable candidates and provider families (ReadStopState),
// and the stop loop that takes them down (StopEntries). It drives the
// lifecycle engine and reads the local inventory, so it sits on the admin
// side of the import graph (internal/depcheck): a session library package
// never links it. The numbered picker a client session prompts with is
// survey.Select, which needs neither.
package hoststop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// stopDeps are `wt stop`'s seams: live inventory, the two live-session
// reads, the batch of stops, the batch's single proxy restart and the pidfile
// read behind a refused port. Tests substitute all six.
type stopDeps struct {
	inventory func(*config.Config) localmodels.Snapshot
	counts    func(modelIDs []string) map[string]int
	// live is the session count of every model id with a live session,
	// unregistered ids included — the family-wide count.
	live func() map[string]int
	// begin starts a batch of stops (lifecycle.BeginStops): it sends every
	// ollama stop at once, and the batch's Stop then finishes one model at a
	// time under one shared wait (#379).
	begin  func(ctx context.Context, cfg *config.Config, entries []localmodels.Entry) stopBatch
	settle func(ctx context.Context, cfg *config.Config)
	// loading reads what the pidfile of a family's server says, for a family
	// whose port refused (lifecycle.LoadingServer). It signals nothing.
	loading func(cfg *config.Config, family string) lifecycle.Loading
	// machine finds the running instance of a ScopeMachine family through the
	// family's own readiness command, on whatever port it listens
	// (lifecycle.MachineEntry): the registry-port probe cannot see one
	// started by hand elsewhere (#457).
	machine func(cfg *config.Config, snap localmodels.Snapshot, family string) (localmodels.Entry, bool)
}

// stopBatch is what the stop loop needs of a lifecycle.StopBatch.
type stopBatch interface {
	// Stop finishes one model's stop and writes its route removal without
	// restarting the proxy; restartOwed reports that settle must run (issue
	// #142). A *lifecycle.StillFinishingError is a stop ollama accepted and
	// has not carried out within the batch's wait.
	Stop(ctx context.Context, en localmodels.Entry) (restartOwed bool, err error)
	// Accepted lists the models whose stop ollama accepted and whose Stop
	// has not reported: what a Ctrl+C leaves ollama to unload by itself.
	Accepted() []localmodels.Entry
}

// defaultStopDeps reads sessions from the map it is given: the local refcount
// store was deleted with Task 19, and a client session's use of a model is the
// model server's to know (the catalog's per-row session count), so the caller
// reads it there and passes it in, by model id (nil: none known, nothing
// asked about). counts and live stay seams for the tests that pin how a count
// would be worded.
func defaultStopDeps(sessions map[string]int) stopDeps {
	return stopDeps{
		inventory: localmodels.Inventory,
		counts: func(ids []string) map[string]int {
			out := map[string]int{}
			for _, id := range ids {
				if n := sessions[id]; n > 0 {
					out[id] = n
				}
			}
			return out
		},
		live: func() map[string]int { return sessions },
		begin: func(ctx context.Context, cfg *config.Config, entries []localmodels.Entry) stopBatch {
			return lifecycle.BeginStops(ctx, cfg, entries)
		},
		settle:  lifecycle.SettleRoutes,
		loading: lifecycle.LoadingServer,
		machine: lifecycle.MachineEntry,
	}
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
	// session (stopDeps.live; none since Task 19 deleted the refcount store),
	// so it does not depend on the candidates: it counts a
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
// not read. None of them is a candidate, so `wt stop` cannot list them.
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
	return ReadStopStateSessions(cfg, nil)
}

// ReadStopStateSessions is ReadStopState with the live client sessions the
// caller read from the model server: the count of sessions using each model
// id. A model one uses is asked about before a stop takes it down.
func ReadStopStateSessions(cfg *config.Config, sessions map[string]int) StopState {
	return stopState(cfg, defaultStopDeps(sessions))
}

func stopState(cfg *config.Config, d stopDeps) StopState {
	snap := d.inventory(cfg)
	st := StopState{Candidates: candidatesFrom(snap, d), Families: map[string]FamilyState{}}
	for fam := range snap.Providers {
		if lifecycle.CanStop(fam) {
			fs := FamilyState{Probed: true, Untrusted: !lifecycle.ProbeTrusted(snap, fam), Down: snap.Down[fam]}
			if c, ok := machineCandidate(cfg, snap, fam, st.Candidates, d); ok {
				// The family's one instance is up on a port the registry does
				// not name: it is a candidate, and its registry port refusing
				// (or not answering) says nothing about it.
				st.Candidates = append(st.Candidates, c)
				fs.Untrusted, fs.Down = false, false
			} else if fs.Down {
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

// machineCandidate is the running instance of a ScopeMachine family that the
// snapshot did not list (#457), as a stop candidate: ok only for a machine
// family none of whose models is already a candidate. Its sessions are the
// whole family's, as for any Exclusive provider.
func machineCandidate(cfg *config.Config, snap localmodels.Snapshot, family string, have []Candidate, d stopDeps) (Candidate, bool) {
	if !lifecycle.MachineScoped(family) {
		return Candidate{}, false
	}
	for _, c := range have {
		if localmodels.Family(c.Entry.ProviderID) == family {
			return Candidate{}, false
		}
	}
	en, ok := d.machine(cfg, snap, family)
	if !ok {
		return Candidate{}, false
	}
	return Candidate{Entry: en, Sessions: d.counts([]string{en.ModelID})[en.ModelID]}, true
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
// var/realfunc shape wt/CLAUDE.md documents for package-level seams. (cmd/wt-session's
// startSignalCtx covers the same ground for the start path but is an inline
// closure, so what the two share is the intent, not the declaration form.)
var stopSignalCtx = realStopSignalCtx

// realStopSignalCtx returns a context cancelled by Ctrl+C or SIGTERM. Without
// a context of its own, a wedged `ollama stop`/`omlx stop`/`mtplx stop` would
// block `wt stop` with no way out but killing the terminal. Cancelling is
// deliberately not fatal — the stop in flight is killed, the rest are skipped,
// and wt still prints its summary.
func realStopSignalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// ErrStopCancelled is what a stop loop returns when Ctrl+C or SIGTERM ended it
// early. It is a value, not only a message, so a caller with more to stop
// after the loop (`wt stop --all`, which halts the omlx service next) can tell
// "the user asked to stop" from "a stop failed" and leave the rest alone.
var ErrStopCancelled = errors.New("cancelled")

// stopEntries is `wt stop`'s stop loop: stops with progress lines, one
// failure never blocks the rest, Ctrl+C (ctx) cancels the stop in flight and
// skips the remainder. Returns nil, a "N of M stops failed" error, or
// ErrStopCancelled.
//
// The stops run as one batch (#379): every ollama stop is sent before the
// first line, then each model is reported in order, an ollama model once
// ollama has unloaded it. The waits share one bound, so ollama models
// finishing long requests cost that bound once, not once each, before the
// models after them and the caller's next step (`wt stop --all`'s halts, the
// exit flow's summary). A wait that has lasted a couple of seconds says so on
// the model's open line (lifecycle.StopNotice), so the line does not look
// hung. A model ollama still lists when the bound is spent is "still
// finishing": ollama accepted the stop and unloads the model when its request
// ends, so it is not a failed stop. A Ctrl+C names the models whose stop
// ollama had already accepted, since ollama still carries those out.
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
	batch := d.begin(ctx, cfg, entries)
	// A wait's notice continues the model's open line ("Stopping x... ").
	stopCtx := lifecycle.WithStopNotice(ctx, func(n lifecycle.StopNotice) {
		fmt.Fprintf(w, "%s... ", n.Msg)
	})
	cancelled := func() error {
		fmt.Fprintln(w, "cancelled")
		if acc := batch.Accepted(); len(acc) > 0 {
			ids := make([]string, 0, len(acc))
			for _, en := range acc {
				ids = append(ids, en.ModelID)
			}
			fmt.Fprintf(w, "wt: ollama had already accepted the stop of %s; it unloads each one once any request in flight ends (`ollama ps` shows when)\n", strings.Join(ids, ", "))
		}
		return ErrStopCancelled
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			// Ctrl+C landed while a stop was in flight (or before the first
			// one): complete the current line and stop offering the rest.
			return cancelled()
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
		owed, err := batch.Stop(stopCtx, e)
		restartOwed = restartOwed || owed
		var finishing *lifecycle.StillFinishingError
		switch {
		case err == nil:
			stoppedFamily[fam] = true
			fmt.Fprintln(w, "done")
		case ctx.Err() != nil:
			// Cancelled mid-stop: the provider CLI was killed, not broken.
			return cancelled()
		case errors.As(err, &finishing):
			// Accepted, and carried out when the request ends: not a
			// failure, and not "done" either — the model is still loaded.
			fmt.Fprintln(w, "still finishing a request; ollama unloads it when that ends")
		default:
			fmt.Fprintf(w, "failed: %v\n", err)
			failed++
		}
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
// path.
func StopEntries(w io.Writer, cfg *config.Config, entries []localmodels.Entry) error {
	ctx, cancel := stopSignalCtx()
	defer cancel()
	return stopEntries(ctx, w, cfg, defaultStopDeps(nil), entries)
}
