// wt-model stop — stop every running model of a provider, or every local
// model, on this machine: `wt-model stop <provider>` and `wt-model stop
// --all` read the host's registry and reuse the hoststop package's stop loop.
// A single model is stopped with `wt stop <model>`, through the model server.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/hoststop"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/spf13/cobra"
)

// stopCmd is `wt-model stop <provider>|--all`. rec is where the running
// server keeps its runtime record: the client sessions the in-use question
// counts are read through it (hostSessionReader).
func stopCmd(h *host, rec servestate.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <provider> | --all",
		Short: "Stop every running model of a provider, or every local model",
		Long: "Stop every running model of a provider (ollama, omlx, omlx-6bit, mtplx, rec,\n" +
			"sushi) on this machine, as its registry names them.\n\n" +
			"On omlx, stopping one model ('wt stop <model>') unloads that model and leaves\n" +
			"the service and its other models up; \"wt-model stop omlx\" stops the service.\n\n" +
			"--all stops every running local model on every provider at once, then the\n" +
			"omlx service and an mtplx it cannot read or that is still loading (below).\n" +
			"It takes no argument. A running mlx_lm_server pairing is not stopped (wt has\n" +
			"no engine for one); \"llmbench provider stop mlx_lm_server\" stops it.\n\n" +
			"\"wt-model stop mtplx\" and --all stop mtplx unless its port refuses the\n" +
			"connection, also when wt cannot tell what it has loaded (an error answer, or\n" +
			"none within 2 seconds). An mtplx that is still loading has not opened its\n" +
			"port: they stop it too, by the pid wt recorded when it started the server,\n" +
			"once that process is confirmed to be the mtplx server on this port (anything\n" +
			"else is left alone). ollama's models are stopped one by one, so when wt\n" +
			"cannot tell what ollama is running it stops nothing there and exits 1.\n\n" +
			"ollama keeps a model that is answering a request loaded until the request\n" +
			"ends. wt sends every ollama stop first and waits up to 3 minutes for all of\n" +
			"them together; a model still loaded then is reported as still finishing,\n" +
			"not as a failed stop, and ollama unloads it when its request ends.\n\n" +
			"It asks before stopping a model that client sessions of the running model\n" +
			"server use (read through serve's runtime record; --yes answers; with no\n" +
			"terminal and no --yes: exit 3, nothing stopped), and also when that server\n" +
			"is running but its sessions cannot be read. With no server running there\n" +
			"is nothing to ask. A model id is stopped with 'wt stop <model>'.\n\n" +
			"Exit 0 stopped (also when nothing was running, or the provider is still\n" +
			"finishing the stop), 1 an argument mistake, a stop that failed or was\n" +
			"cancelled with Ctrl+C, or a provider wt could not read, 3 approval missing.",
		Example:      "  wt-model stop ollama\n  wt-model stop omlx\n  wt-model stop --all --yes",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			yes, _ := cmd.Flags().GetBool("yes")
			all, _ := cmd.Flags().GetBool("all")
			// A model is the model server's to stop, from wt: wt-model
			// stops providers.
			if strings.Contains(arg, "/") {
				return fmt.Errorf("%q is a model id: wt-model stop stops a provider or --all; stop one model with 'wt stop %s'", arg, arg)
			}
			if arg == "" && !all {
				return errors.New("wt-model stop needs a provider or --all; stop one model with 'wt stop <model>'")
			}
			cfg, _ := h.hostConfig()
			if cerr := h.hostConfigErr(); cerr != nil {
				return config.ConfigError(cerr)
			}
			h.stopCtx = cmd.Context()
			h.stopSessionsUnread = nil
			h.stopSessions = hostSessionReader(cmd.Context(), cmd.ErrOrStderr(), rec, &h.stopSessionsUnread)
			if all {
				if arg != "" {
					return fmt.Errorf("wt-model stop --all takes no provider (got %q)", arg)
				}
				return h.runStopAll(cmd.OutOrStdout(), cfg, yes)
			}
			return h.runStop(cmd.OutOrStdout(), cfg, arg, yes)
		},
	}
	cmd.Flags().Bool("yes", false, "Stop models that client sessions of the running model server use, without asking")
	cmd.Flags().Bool("all", false, "Stop every running local model, then the omlx service and an mtplx wt cannot read or that is still loading")
	return cmd
}

// haltsWhole reports whether stopping providerID with the given number of
// stop candidates in its family means halting its server as a whole
// (haltProvider) rather than stopping models one by one. A Pool (omlx) always
// is: unloading its models leaves the service and its memory in place. An
// Exclusive provider (mtplx) is when the probe gave no candidate to stop it
// through and its port did not refuse — a server that answered with nothing
// loaded, with an error or not in time is a server that is up (#308). A refused port is
// nothing to stop unless wt's pidfile names a live process it verified as the
// server (FamilyState.Loading): mtplx reads its weights before it opens its
// port, so that is a server still loading. A family the inventory never
// probed (FamilyState.Probed) is nothing to stop either: "did not refuse" is
// an answer only when wt asked. A Shared provider (ollama) never is: wt
// leaves its daemon up.
func haltsWhole(st hoststop.StopState, providerID string, candidates int) bool {
	switch lifecycle.TenancyOf(providerID) {
	case lifecycle.Pool:
		return true
	case lifecycle.Exclusive:
		fs := st.Families[localmodels.Family(providerID)]
		return candidates == 0 && fs.Probed && (!fs.Down || fs.Loading.PID > 0)
	}
	return false
}

// haltProviders lists, in id order, one configured provider id per server
// `wt-model stop --all` halts as a whole (haltsWhole): "omlx" and "omlx-6bit"
// are one service, and it is halted once.
func haltProviders(cfg *config.Config, st hoststop.StopState) []string {
	perFamily := map[string]int{}
	for _, c := range st.Candidates {
		perFamily[localmodels.Family(c.Entry.ProviderID)]++
	}
	var ids []string
	seen := map[string]bool{}
	for _, id := range stoppableProviders(cfg) {
		fam := localmodels.Family(id)
		if seen[fam] || !haltsWhole(st, id, perFamily[fam]) {
			continue
		}
		seen[fam] = true
		ids = append(ids, id)
	}
	return ids
}

// unreadShared lists, in id order, the configured Shared families (ollama)
// whose probe was neither trusted nor refused. wt stops their models one by
// one and so can stop nothing there, and may not report them as idle.
func unreadShared(cfg *config.Config, st hoststop.StopState) []string {
	var fams []string
	for _, id := range stoppableProviders(cfg) {
		fam := localmodels.Family(id)
		if fs := st.Families[fam]; lifecycle.TenancyOf(id) == lifecycle.Shared && fs.Untrusted && !fs.Down && !slices.Contains(fams, fam) {
			fams = append(fams, fam)
		}
	}
	return fams
}

// haltImpact is what halting a family's server costs the live wt sessions:
// how many use a model of the family, which models, and whether wt is asking
// blind (the probe was not trusted). A server whose port refused serves
// nobody, so halting it costs nothing whatever sessions are counted
// — unless it is still loading (FamilyState.Loading): the sessions on its
// family are then waiting for that load, and wt is not asking blind, since it
// knows what is there.
func haltImpact(fs hoststop.FamilyState) (sessions int, users []string, blind bool) {
	if fs.Down {
		if fs.Loading.PID > 0 {
			return fs.Sessions, fs.Users, false
		}
		return 0, nil, false
	}
	return fs.Sessions, fs.Users, fs.Untrusted
}

// errLoadingUnknown is the error of a stop that found a live pid in a
// provider's pidfile behind a refused port and could not read the process
// table to say what it is: neither "nothing running" nor something wt may
// signal. hint ends it with what the command did, as errCannotTell's does: a
// command that stopped other things must not say nothing was stopped.
func errLoadingUnknown(provider string, err error, hint string) error {
	return fmt.Errorf("cannot tell whether %s is still loading: %w%s", provider, err, hint)
}

// pidfileFindings is what the pidfiles behind refused ports name
// (hoststop.StopState.Loading), one entry per family in provider order: an
// error for a process table wt could not read (ending in hint) and a note for
// a live process that is not the server (left alone). A verified server still
// loading gets nothing here: `wt-model stop --all`, the one caller, stops it.
func pidfileFindings(cfg *config.Config, loading map[string]lifecycle.Loading, hint string) (notes []string, errs []error) {
	seen := map[string]bool{}
	for _, id := range stoppableProviders(cfg) {
		fam := localmodels.Family(id)
		if seen[fam] {
			continue
		}
		seen[fam] = true
		switch l := loading[fam]; {
		case l.Err != nil:
			errs = append(errs, errLoadingUnknown(id, l.Err, hint))
		case l.Stray != "":
			notes = append(notes, l.Stray)
		}
	}
	return notes, errs
}

// errCannotTell is the error of a stop that could not read what the named
// providers are running and has no way to stop them regardless.
func errCannotTell(families []string, hint string) error {
	return fmt.Errorf("cannot tell what is running on %s: the probe gave no usable answer%s", strings.Join(families, ", "), hint)
}

// unreadClause is the part of the in-use question that says wt is asking
// blind: the providers about to be halted whose probe was not trusted. ""
// when every one was read, which keeps the question's usual wording.
func unreadClause(names []string, it bool) string {
	switch {
	case len(names) == 0:
		return ""
	case it:
		return ", and wt could not tell what it has loaded"
	case len(names) == 1:
		return fmt.Sprintf(", and wt could not tell what %s has loaded", names[0])
	}
	return fmt.Sprintf(", and wt could not tell what %s and %s have loaded", strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// runStopAll implements `wt-model stop --all`: every running local model wt
// can stop, then each server it halts as a whole (haltsWhole). A pool's models
// go down with their service, so they are not unloaded one by one first. Like
// `wt-model stop omlx`, it halts a configured pool service even when no model
// is loaded: that is what frees the server's memory. An Exclusive provider
// (mtplx) with no candidate is halted too unless its port refused, so "all"
// does not depend on what a probe managed to read (#308) — and behind a
// refused port when its pidfile names the verified server, still loading. Every stop is
// attempted; a failure does not leave the rest running, and the command then
// exits non-zero.
//
// The in-use question covers the halts with each halted family's own session
// count (hoststop.FamilyState), which no candidate has to carry (#307).
//
// Ctrl+C is not a failure to step over: it ends the command where it is. A
// model loop that was cancelled leaves the halts alone, and a cancelled halt
// is not followed by another.
func (h *host) runStopAll(out io.Writer, cfg *config.Config, yes bool) error {
	st := h.stopState(cfg)
	if err := h.stopCancelled(); err != nil {
		return err
	}
	halts := haltProviders(cfg, st)
	halted := map[string]bool{}
	for _, id := range halts {
		halted[localmodels.Family(id)] = true
	}
	// A halted family's models go down with its server; the rest are stopped
	// one by one.
	var entries []localmodels.Entry
	var oneByOne []hoststop.Candidate
	for _, c := range st.Candidates {
		if !halted[localmodels.Family(c.Entry.ProviderID)] {
			entries = append(entries, c.Entry)
			oneByOne = append(oneByOne, c)
		}
	}
	inUse, users := stopImpact(oneByOne)
	var unread []string
	loading := ""
	for _, id := range halts {
		fs := st.Families[localmodels.Family(id)]
		n, ids, blind := haltImpact(fs)
		inUse += n
		users = append(users, ids...)
		if blind {
			unread = append(unread, id)
		}
		if fs.Loading.PID > 0 {
			loading += fmt.Sprintf(", and %s is still loading (pid %d)", id, fs.Loading.PID)
		}
	}
	if (inUse > 0 || h.stopSessionsUnread != nil) && !yes {
		q := fmt.Sprintf("running local models are in use by %d live wt session(s) (%s)%s%s; stop them all?", inUse, strings.Join(users, ", "), unreadClause(unread, false), loading)
		if inUse == 0 {
			q = fmt.Sprintf("running local models may be in use: the running model server's client sessions could not be read%s%s; stop them all?", unreadClause(unread, false), loading)
		}
		ok, err := h.confirmStop(q)
		if err != nil {
			return cli.NoTerminalRefusal(err, "--yes", "nothing was stopped")
		}
		if !ok {
			return cli.Declined("nothing was stopped")
		}
	}
	if err := h.stopCancelled(); err != nil {
		return err
	}
	var failures []error
	if fams := unreadShared(cfg, st); len(fams) > 0 {
		failures = append(failures, errCannotTell(fams, " — nothing was stopped there"))
	}
	// What a pidfile behind a refused port names that wt will not signal:
	// said once everything else is, so it is the last thing on the screen.
	notes, unknown := pidfileFindings(cfg, st.Loading(), " — nothing was stopped there")
	failures = append(failures, unknown...)
	defer func() {
		for _, n := range notes {
			fmt.Fprintf(out, "wt-model: %s\n", n)
		}
	}()
	if len(entries) == 0 && len(halts) == 0 {
		if len(failures) == 0 {
			fmt.Fprintln(out, "wt-model: no running local models")
		}
		return errors.Join(failures...)
	}
	// One signal context for the whole command, installed before the model
	// loop. The loop runs under a context of its own (hoststop.StopEntries) and
	// releases its handler when it returns, so with none installed here a
	// Ctrl+C that lands as the last model stop completes is seen by nobody —
	// the loop returns nil and the command goes on to halt the pool — and one
	// during the wait below would kill wt-model outright, mid proxy restart. A
	// signal reaches both contexts; this one is read by the first pass of the
	// halt loop and reported.
	ctx, cancel := h.startSignalCtx()
	defer cancel()
	if len(entries) > 0 {
		if err := h.stopEntries(out, cfg, entries); err != nil {
			if errors.Is(err, hoststop.ErrStopCancelled) {
				return notHalted(halts)
			}
			failures = append(failures, err)
		}
	}
	// The model loop may have started a LiteLLM proxy restart (a stopped
	// mtplx model's route), and halting a server starts its own. They run
	// asynchronously and nothing else orders them, so each is finished
	// before the next halt: two at once restart the proxy under each other.
	restartPending := len(entries) > 0
	for i, id := range halts {
		if restartPending {
			h.waitPendingRoutes()
		}
		restartPending = true
		if ctx.Err() != nil {
			failures = append(failures, notHalted(halts[i:]))
			break
		}
		cancelled, err := h.haltProvider(ctx, out, cfg, id, st.Families[localmodels.Family(id)].Loading.PID)
		if cancelled {
			failures = append(failures, haltCancelled(err, halts[i:]))
			break
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

// haltProvider stops one provider's server as a whole (`wt-model stop omlx`,
// `wt-model stop mtplx` with no candidate, and each halt of `wt-model stop
// --all`) and reports it on out. loadingPID > 0 is a server that has not
// opened its port (FamilyState.Loading): it is stopped by that pid, since its
// provider's own stop goes through the port, and the line says so. cancelled
// means Ctrl+C ended it: the provider's stop was killed, not broken, so it is
// printed as "cancelled" and the caller reports it as one, not as a failed
// stop.
func (h *host) haltProvider(ctx context.Context, out io.Writer, cfg *config.Config, id string, loadingPID int) (cancelled bool, err error) {
	if loadingPID > 0 {
		fmt.Fprintf(out, "Stopping %s (still loading, pid %d)... ", id, loadingPID)
		err = h.stopLoading(ctx, cfg, id, loadingPID)
	} else {
		fmt.Fprintf(out, "Stopping %s... ", id)
		err = h.stopProvider(ctx, cfg, id)
	}
	switch {
	case err == nil:
		fmt.Fprintln(out, "done")
	case ctx.Err() != nil:
		fmt.Fprintln(out, "cancelled")
		return true, err
	default:
		fmt.Fprintln(out, "failed")
	}
	return false, err
}

// stopCancelled is the error of a stop whose command context Ctrl+C
// cancelled before anything was stopped (h.stopCtx; main runs the tree under
// signal.NotifyContext, so the first Ctrl+C only cancels it), nil otherwise.
// It is checked once the client sessions and the inventory are read and once
// the in-use question is answered: a session read cut short knows no
// sessions, and going on would stop models client sessions use without
// asking after the user asked to abort.
func (h *host) stopCancelled() error {
	if h.stopCtx == nil || h.stopCtx.Err() == nil {
		return nil
	}
	return fmt.Errorf("%w — nothing was stopped", hoststop.ErrStopCancelled)
}

// notHalted is the error of a stop cut short by Ctrl+C: it names the servers
// the command did not halt, so "cancelled" alone cannot be read as
// "everything else is down".
func notHalted(ids []string) error {
	if len(ids) == 0 {
		return hoststop.ErrStopCancelled
	}
	return fmt.Errorf("%w — %s was not stopped", hoststop.ErrStopCancelled, strings.Join(ids, ", "))
}

// haltCancelled is the error of a halt of ids[0] that Ctrl+C ended (err is
// what the halt returned), with ids[1:] the halts that were to follow. A
// loading server wt had already signalled is not "not stopped": it has the
// signal and, unless it ignores it, exits a moment later
// (*lifecycle.StopInterruptedError). The error says that and how to check;
// the servers after it were not touched, and are named as notHalted names
// them.
func haltCancelled(err error, ids []string) error {
	var sent *lifecycle.StopInterruptedError
	if !errors.As(err, &sent) {
		return notHalted(ids)
	}
	id := ids[0]
	signalled := fmt.Errorf("%w — %s (pid %d) was sent %s and wt did not wait for it to exit; run \"wt-model stop %s\" again to confirm", hoststop.ErrStopCancelled, id, sent.PID, sent.Signal, id)
	if len(ids) == 1 {
		return signalled
	}
	return errors.Join(signalled, notHalted(ids[1:]))
}

// stoppableProviders lists, sorted, the configured provider ids wt can stop.
// The bare-provider check and its error message both read it, so they cannot
// disagree about what is valid.
func stoppableProviders(cfg *config.Config) []string {
	var ids []string
	for _, p := range cfg.Providers {
		if lifecycle.CanStop(p.ID) {
			ids = append(ids, p.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// stopArgError is the mistake in `wt-model stop <provider>`'s argument, nil
// when there is none: a bare provider wt cannot stop. It probes nothing.
func stopArgError(cfg *config.Config, arg string) error {
	if valid := stoppableProviders(cfg); !slices.Contains(valid, arg) {
		return fmt.Errorf("unknown provider %q (valid: %s)", arg, strings.Join(valid, ", "))
	}
	return nil
}

// runStop implements `wt-model stop <provider>` (a model id is `wt stop
// <model>`'s, through the model server). Argument errors return before any
// side effect.
func (h *host) runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error {
	if err := stopArgError(cfg, arg); err != nil {
		return err
	}
	st := h.stopState(cfg)
	if err := h.stopCancelled(); err != nil {
		return err
	}
	cands := st.Candidates
	var targets []hoststop.Candidate
	pool := lifecycle.TenancyOf(arg) == lifecycle.Pool
	for _, c := range cands {
		// A pool's server is stopped as a whole, so every model of the
		// family is affected, whichever of its rows it is listed under.
		if c.Entry.ProviderID == arg || (pool && localmodels.Family(c.Entry.ProviderID) == localmodels.Family(arg)) {
			targets = append(targets, c)
		}
	}
	// halt: the provider's server is stopped as a whole, not model by model.
	halt := haltsWhole(st, arg, len(targets))
	if len(targets) == 0 && !halt {
		fs := st.Families[localmodels.Family(arg)]
		if fs.Untrusted && !fs.Down {
			// Only a Shared provider gets here: its models are stopped
			// one by one, so there is nothing to stop blind.
			return errCannotTell([]string{arg}, " — nothing was stopped")
		}
		if fs.Loading.Err != nil {
			return errLoadingUnknown(arg, fs.Loading.Err, " — nothing was stopped")
		}
		fmt.Fprintf(out, "wt-model: nothing running on %s\n", arg)
		if fs.Loading.Stray != "" {
			fmt.Fprintf(out, "wt-model: %s\n", fs.Loading.Stray)
		}
		return nil
	}

	targets = withFamilyCollateral(out, cands, targets)
	entries := make([]localmodels.Entry, 0, len(targets))
	for _, c := range targets {
		entries = append(entries, c.Entry)
	}
	inUse, users := stopImpact(targets)
	blind, what, loadingPID := "", arg+" is", 0
	if halt {
		// A halt takes every model of the family down, so it is the family's
		// sessions that count — whatever the candidates were (#307).
		fs := st.Families[localmodels.Family(arg)]
		var unread bool
		inUse, users, unread = haltImpact(fs)
		if unread {
			blind = unreadClause([]string{arg}, true)
		}
		if loadingPID = fs.Loading.PID; loadingPID > 0 {
			what = fmt.Sprintf("%s is still loading (pid %d) and is", arg, loadingPID)
		}
	}
	if (inUse > 0 || h.stopSessionsUnread != nil) && !yes {
		q := fmt.Sprintf("%s in use by %d live wt session(s) (%s)%s; stop anyway?", what, inUse, strings.Join(users, ", "), blind)
		if inUse == 0 {
			q = fmt.Sprintf("%s possibly in use: the running model server's client sessions could not be read%s; stop anyway?", what, blind)
		}
		ok, err := h.confirmStop(q)
		if err != nil {
			return cli.NoTerminalRefusal(err, "--yes", "nothing was stopped")
		}
		if !ok {
			return cli.Declined(arg + " is still running")
		}
	}
	if err := h.stopCancelled(); err != nil {
		return err
	}
	if halt {
		// A model stop on a pool only unloads; the bare provider halts the
		// service, which is the one way to free the server itself. An
		// Exclusive provider with no candidate is halted the same way.
		ctx, cancel := h.startSignalCtx()
		defer cancel()
		cancelled, err := h.haltProvider(ctx, out, cfg, arg, loadingPID)
		if cancelled {
			return haltCancelled(err, []string{arg})
		}
		return err
	}
	return h.stopEntries(out, cfg, entries)
}

// withFamilyCollateral widens targets to every running model of any
// Exclusive provider (mtplx) they touch: that provider stops as a whole, so a
// sibling variant goes down with the one named. A Pool (omlx) is not widened:
// stopping one of its models leaves the others loaded. Collateral models are
// announced on out. Targets keep their order; collateral follows.
func withFamilyCollateral(out io.Writer, cands, targets []hoststop.Candidate) []hoststop.Candidate {
	have := map[string]bool{}
	fams := map[string]bool{}
	for _, c := range targets {
		have[c.Entry.ModelID] = true
		if lifecycle.TenancyOf(c.Entry.ProviderID) == lifecycle.Exclusive {
			fams[localmodels.Family(c.Entry.ProviderID)] = true
		}
	}
	for _, c := range cands {
		if have[c.Entry.ModelID] || lifecycle.TenancyOf(c.Entry.ProviderID) != lifecycle.Exclusive || !fams[localmodels.Family(c.Entry.ProviderID)] {
			continue
		}
		fmt.Fprintf(out, "wt-model: %s runs one model per process; stopping it also stops %s\n", localmodels.Family(c.Entry.ProviderID), c.Entry.ModelID)
		targets = append(targets, c)
	}
	return targets
}

// stopImpact totals the live wt sessions a stop would hit and names the models
// they use. A single-model provider's candidates each carry the whole family's
// count, so the family is counted once; other candidates sum.
func stopImpact(targets []hoststop.Candidate) (sessions int, users []string) {
	famSeen := map[string]bool{}
	for _, c := range targets {
		if c.Sessions == 0 {
			continue
		}
		users = append(users, c.Entry.ModelID)
		if lifecycle.TenancyOf(c.Entry.ProviderID) == lifecycle.Exclusive {
			fam := localmodels.Family(c.Entry.ProviderID)
			if famSeen[fam] {
				continue
			}
			famSeen[fam] = true
		}
		sessions += c.Sessions
	}
	return sessions, users
}
