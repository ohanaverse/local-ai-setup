// wt start / wt stop — direct control of local models without launching an
// agent. Start reuses the non-TUI start driver (startModel); stop reuses the
// survey package's stop loop and picker. wt served reports what a provider's
// server is serving, the probe the other two act on.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/spf13/cobra"
)

// Test seams: production talks to the live inventory/providers; cmd/wt's
// TestMain stubs all of them.
var (
	// stopState is one inventory round and one read of the live sessions: the
	// stop candidates, and per provider family what its probe said and how
	// many live wt sessions use it.
	stopState   = survey.ReadStopState
	stopEntries = survey.StopEntries
	// stopPickerAll runs the in-use-inclusive picker and reports whether it had
	// anything to offer and which families it could not read, so `wt stop`
	// needs no inventory probe of its own.
	stopPickerAll = func(cfg *config.Config) (bool, []string) {
		return survey.PickerWith(os.Stdin, os.Stdout, cfg, survey.Options{IncludeInUse: true})
	}
	confirmStop = promptStop
	// stopProvider stops a provider's server as a whole (`wt stop omlx`).
	stopProvider = lifecycle.Stop
	// stopLoading ends a provider's server that has not opened its port yet
	// (an mtplx still reading its weights), by the pid the stop state
	// verified.
	stopLoading = lifecycle.StopLoading
)

// promptStop is promptReplace's twin for stopping an in-use model, and what
// `wt model rm` asks with too: y/N on the controlling terminal, default No,
// so piped input can never authorise it. The terminal is opened through the
// openTTY seam, so a test can take it away.
func promptStop(question string) (bool, error) {
	f, err := openTTY()
	if err != nil {
		return false, fmt.Errorf("%s — rerun with --yes to confirm", question)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return askYesNo(f)
}

func stopCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop [model|provider]",
		Short: "Stop a running local model, every model of a provider, or all of them",
		Long: "Stop a running local model (<provider>/<name>) or every running model of a\n" +
			"provider (ollama, omlx, omlx-6bit, mtplx). With no argument, shows the\n" +
			"stop picker (requires a TTY), which also lists models other wt sessions\n" +
			"are using, marked with their session count.\n\n" +
			"On omlx, stopping a model unloads that model and leaves the service and its other\n" +
			"models up; \"wt stop omlx\" stops the service.\n\n" +
			"--all stops every running local model on every provider at once, then the\n" +
			"omlx service and an mtplx it cannot read or that is still loading (below).\n" +
			"It takes no argument. A running mlx_lm_server pairing is not stopped (wt has\n" +
			"no engine for one); \"llmbench provider stop mlx_lm_server\" stops it.\n\n" +
			"\"wt stop mtplx\" and --all stop mtplx unless its port refuses the connection,\n" +
			"also when wt cannot tell what it has loaded (an error answer, or none within\n" +
			"2 seconds). An mtplx that is still loading has not opened its port: they\n" +
			"stop it too, by the pid wt recorded when it started the server, once that\n" +
			"process is confirmed to be the mtplx server on this port (anything else is\n" +
			"left alone). ollama's models are stopped one by one, so when wt cannot tell\n" +
			"what ollama is running it stops nothing there and exits 1.\n\n" +
			"Stopping a model or a provider a live wt session uses asks for confirmation;\n" +
			"--yes skips it.",
		Example: "  wt stop ollama/qwen3.8:27b-mlx\n  wt stop ollama\n  wt stop\n  wt stop --all --yes",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return configError(a.cfgErr)
			}
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			yes, _ := cmd.Flags().GetBool("yes")
			if all, _ := cmd.Flags().GetBool("all"); all {
				if arg != "" {
					return fmt.Errorf("wt stop --all takes no model or provider (got %q)", arg)
				}
				// Past the argument check nothing --all returns is a usage
				// mistake: a failed stop, a declined question or Ctrl+C would
				// otherwise bury its progress lines under the whole usage text.
				cmd.SilenceUsage = true
				return runStopAll(cmd.OutOrStdout(), a.cfg, yes)
			}
			if err := stopArgError(a.cfg, arg); err != nil {
				return err
			}
			// As for --all: past the argument check a failure is a stop
			// that failed, a declined question or a provider wt could not
			// read, never a usage mistake.
			cmd.SilenceUsage = true
			return runStop(cmd.OutOrStdout(), a.cfg, arg, yes)
		},
	}
	cmd.Flags().Bool("yes", false, "Skip the confirmation when a target is in use by a live wt session")
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
func haltsWhole(st survey.StopState, providerID string, candidates int) bool {
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
// `wt stop --all` halts as a whole (haltsWhole): "omlx" and "omlx-6bit" are
// one service, and it is halted once.
func haltProviders(cfg *config.Config, st survey.StopState) []string {
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
func unreadShared(cfg *config.Config, st survey.StopState) []string {
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
// nobody, so halting it costs nothing whatever the refcount file still lists
// — unless it is still loading (FamilyState.Loading): the sessions on its
// family are then waiting for that load, and wt is not asking blind, since it
// knows what is there.
func haltImpact(fs survey.FamilyState) (sessions int, users []string, blind bool) {
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
// signal.
func errLoadingUnknown(provider string, err error) error {
	return fmt.Errorf("cannot tell whether %s is still loading: %w — nothing was stopped", provider, err)
}

// pidfileFindings is what the pidfiles behind refused ports name besides a
// verified server, one entry per family in provider order: a note for a live
// process that is not the server (left alone), an error for a process table
// wt could not read.
func pidfileFindings(cfg *config.Config, st survey.StopState) (notes []string, errs []error) {
	seen := map[string]bool{}
	for _, id := range stoppableProviders(cfg) {
		fam := localmodels.Family(id)
		if seen[fam] {
			continue
		}
		seen[fam] = true
		switch l := st.Families[fam].Loading; {
		case l.Err != nil:
			errs = append(errs, errLoadingUnknown(id, l.Err))
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

// unreadHint is what bare `wt stop` adds after naming the families it could
// not read: the commands that stop such a server regardless, named only for
// the families wt halts as a whole. A Shared family (ollama) has no such
// command — `wt stop ollama` and `wt stop --all` stop nothing on an ollama wt
// cannot read — so a list of only those gets "" and the caller's own ending.
func unreadHint(cfg *config.Config, unknown []string) string {
	var cmds, fams []string
	for _, id := range stoppableProviders(cfg) {
		fam := localmodels.Family(id)
		if lifecycle.TenancyOf(id) == lifecycle.Shared || !slices.Contains(unknown, fam) || slices.Contains(fams, fam) {
			continue
		}
		fams = append(fams, fam)
		cmds = append(cmds, fmt.Sprintf("%q", "wt stop "+id))
	}
	if len(fams) == 0 {
		return ""
	}
	return fmt.Sprintf(" — %s or \"wt stop --all\" stops %s", strings.Join(cmds, ", "), strings.Join(fams, " and "))
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

// runStopAll implements `wt stop --all`: every running local model wt can
// stop, then each server it halts as a whole (haltsWhole). A pool's models go
// down with their service, so they are not unloaded one by one first. Like
// `wt stop omlx`, it halts a configured pool service even when no model is
// loaded: that is what frees the server's memory. An Exclusive provider
// (mtplx) with no candidate is halted too unless its port refused, so "all"
// does not depend on what a probe managed to read (#308) — and behind a
// refused port when its pidfile names the verified server, still loading. Every stop is
// attempted; a failure does not leave the rest running, and the command then
// exits non-zero.
//
// The in-use question covers the halts with each halted family's own session
// count (survey.FamilyState), which no candidate has to carry (#307).
//
// Ctrl+C is not a failure to step over: it ends the command where it is. A
// model loop that was cancelled leaves the halts alone, and a cancelled halt
// is not followed by another.
func runStopAll(out io.Writer, cfg *config.Config, yes bool) error {
	st := stopState(cfg)
	halts := haltProviders(cfg, st)
	halted := map[string]bool{}
	for _, id := range halts {
		halted[localmodels.Family(id)] = true
	}
	// A halted family's models go down with its server; the rest are stopped
	// one by one.
	var entries []localmodels.Entry
	var oneByOne []survey.Candidate
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
	if inUse > 0 && !yes {
		ok, err := confirmStop(fmt.Sprintf("running local models are in use by %d live wt session(s) (%s)%s%s; stop them all?", inUse, strings.Join(users, ", "), unreadClause(unread, false), loading))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled — nothing was stopped")
		}
	}
	var failures []error
	if fams := unreadShared(cfg, st); len(fams) > 0 {
		failures = append(failures, errCannotTell(fams, " — nothing was stopped there"))
	}
	// What a pidfile behind a refused port names that wt will not signal:
	// said once everything else is, so it is the last thing on the screen.
	notes, unknown := pidfileFindings(cfg, st)
	failures = append(failures, unknown...)
	defer func() {
		for _, n := range notes {
			fmt.Fprintf(out, "wt: %s\n", n)
		}
	}()
	if len(entries) == 0 && len(halts) == 0 {
		if len(failures) == 0 {
			fmt.Fprintln(out, "wt: no running local models")
		}
		return errors.Join(failures...)
	}
	// One signal context for the whole command, installed before the model
	// loop. The loop runs under a context of its own (survey.StopEntries) and
	// releases its handler when it returns, so with none installed here a
	// Ctrl+C that lands as the last model stop completes is seen by nobody —
	// the loop returns nil and the command goes on to halt the pool — and one
	// during the wait below would kill wt outright, mid proxy restart. A
	// signal reaches both contexts; this one is read by the first pass of the
	// halt loop and reported.
	ctx, cancel := startSignalCtx()
	defer cancel()
	if len(entries) > 0 {
		if err := stopEntries(out, cfg, entries); err != nil {
			if errors.Is(err, survey.ErrStopCancelled) {
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
			waitPendingRoutes()
		}
		restartPending = true
		if ctx.Err() != nil {
			failures = append(failures, notHalted(halts[i:]))
			break
		}
		cancelled, err := haltProvider(ctx, out, cfg, id, st.Families[localmodels.Family(id)].Loading.PID)
		if cancelled {
			failures = append(failures, notHalted(halts[i:]))
			break
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

// haltProvider stops one provider's server as a whole (`wt stop omlx`, `wt
// stop mtplx` with no candidate, and each halt of `wt stop --all`) and
// reports it on out. loadingPID > 0 is a server that has not opened its port
// (FamilyState.Loading): it is stopped by that pid, since its provider's own
// stop goes through the port, and the line says so. cancelled means Ctrl+C
// ended it: the provider's stop was killed, not broken, so it is printed as
// "cancelled" and the caller reports it as one, not as a failed stop.
func haltProvider(ctx context.Context, out io.Writer, cfg *config.Config, id string, loadingPID int) (cancelled bool, err error) {
	if loadingPID > 0 {
		fmt.Fprintf(out, "Stopping %s (still loading, pid %d)... ", id, loadingPID)
		err = stopLoading(ctx, cfg, id, loadingPID)
	} else {
		fmt.Fprintf(out, "Stopping %s... ", id)
		err = stopProvider(ctx, cfg, id)
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

// notHalted is the error of a stop cut short by Ctrl+C: it names the servers
// the command did not halt, so "cancelled" alone cannot be read as
// "everything else is down".
func notHalted(ids []string) error {
	if len(ids) == 0 {
		return survey.ErrStopCancelled
	}
	return fmt.Errorf("%w — %s was not stopped", survey.ErrStopCancelled, strings.Join(ids, ", "))
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

// stopArgError is the usage mistake in `wt stop`'s argument, nil when there is
// none: a bare provider wt cannot stop, or no argument without a terminal for
// the picker. It probes nothing; a model id still needs the live inventory to
// find its running entry.
func stopArgError(cfg *config.Config, arg string) error {
	if arg != "" && !strings.Contains(arg, "/") {
		if valid := stoppableProviders(cfg); !slices.Contains(valid, arg) {
			return fmt.Errorf("unknown provider %q (valid: %s)", arg, strings.Join(valid, ", "))
		}
	}
	if arg == "" && !stdinTTY() {
		return fmt.Errorf("wt stop needs a TTY to list models; pass a model or provider (wt stop <provider>/<name> | wt stop <provider>)")
	}
	return nil
}

// runStop implements `wt stop`. Argument errors return before any side effect.
func runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error {
	if err := stopArgError(cfg, arg); err != nil {
		return err
	}
	if arg == "" {
		// The picker takes the one inventory snapshot itself and reports whether
		// it had anything to offer, so nothing is probed twice.
		offered, unknown := stopPickerAll(cfg)
		hint := unreadHint(cfg, unknown)
		switch {
		case offered && len(unknown) > 0:
			// The list was not everything that may be running.
			fmt.Fprintf(out, "wt: could not tell what is running on %s%s\n", strings.Join(unknown, ", "), hint)
		case offered:
		case len(unknown) > 0:
			// Nothing to list is not "nothing running" when a probe gave
			// no usable answer.
			if hint == "" {
				hint = " — nothing was stopped"
			}
			return errCannotTell(unknown, hint)
		default:
			fmt.Fprintln(out, "wt: no running local models")
		}
		return nil
	}
	st := stopState(cfg)
	cands := st.Candidates
	// halt: the provider's server is stopped as a whole, not model by model.
	halt := false

	var targets []survey.Candidate
	if strings.Contains(arg, "/") {
		for _, c := range cands {
			if c.Entry.ModelID == arg {
				targets = append(targets, c)
			}
		}
		if len(targets) == 0 {
			if i := config.IndexModelByID(cfg.Models, arg); i >= 0 {
				// wt does not know which model a server that is still
				// loading holds, so a model id stops nothing there; the
				// provider does.
				prov := cfg.Models[i].ProviderID
				if pid := st.Families[localmodels.Family(prov)].Loading.PID; pid > 0 {
					return fmt.Errorf("model %q is not running — %s is still loading (pid %d); \"wt stop %s\" stops it", arg, prov, pid, prov)
				}
				return fmt.Errorf("model %q is not running", arg)
			}
			return fmt.Errorf("unknown model %q", arg)
		}
	} else {
		pool := lifecycle.TenancyOf(arg) == lifecycle.Pool
		for _, c := range cands {
			// A pool's server is stopped as a whole, so every model of the
			// family is affected, whichever of its rows it is listed under.
			if c.Entry.ProviderID == arg || (pool && localmodels.Family(c.Entry.ProviderID) == localmodels.Family(arg)) {
				targets = append(targets, c)
			}
		}
		halt = haltsWhole(st, arg, len(targets))
		if len(targets) == 0 && !halt {
			fs := st.Families[localmodels.Family(arg)]
			if fs.Untrusted && !fs.Down {
				// Only a Shared provider gets here: its models are stopped
				// one by one, so there is nothing to stop blind.
				return errCannotTell([]string{arg}, " — nothing was stopped")
			}
			if fs.Loading.Err != nil {
				return errLoadingUnknown(arg, fs.Loading.Err)
			}
			fmt.Fprintf(out, "wt: nothing running on %s\n", arg)
			if fs.Loading.Stray != "" {
				fmt.Fprintf(out, "wt: %s\n", fs.Loading.Stray)
			}
			return nil
		}
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
	if inUse > 0 && !yes {
		ok, err := confirmStop(fmt.Sprintf("%s in use by %d live wt session(s) (%s)%s; stop anyway?", what, inUse, strings.Join(users, ", "), blind))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("cancelled — %s is still running", arg)
		}
	}
	if halt {
		// A model stop on a pool only unloads; the bare provider halts the
		// service, which is the one way to free the server itself. An
		// Exclusive provider with no candidate is halted the same way.
		ctx, cancel := startSignalCtx()
		defer cancel()
		cancelled, err := haltProvider(ctx, out, cfg, arg, loadingPID)
		if cancelled {
			return notHalted([]string{arg})
		}
		return err
	}
	return stopEntries(out, cfg, entries)
}

// withFamilyCollateral widens targets to every running model of any
// Exclusive provider (mtplx) they touch: that provider stops as a whole, so a
// sibling variant goes down with the one named. A Pool (omlx) is not widened:
// stopping one of its models leaves the others loaded. Collateral models are
// announced on out. Targets keep their order; collateral follows.
func withFamilyCollateral(out io.Writer, cands, targets []survey.Candidate) []survey.Candidate {
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
		fmt.Fprintf(out, "wt: %s runs one model per process; stopping it also stops %s\n", localmodels.Family(c.Entry.ProviderID), c.Entry.ModelID)
		targets = append(targets, c)
	}
	return targets
}

// stopImpact totals the live wt sessions a stop would hit and names the models
// they use. A single-model provider's candidates each carry the whole family's
// count, so the family is counted once; other candidates sum.
func stopImpact(targets []survey.Candidate) (sessions int, users []string) {
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

func startCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "start [model]",
		Short: "Start a local model",
		Long: "Start a local model (<provider>/<name>, as with -M). With no argument, shows\n" +
			"the full model picker over every local model that is on disk or running,\n" +
			"registered or detected (requires a TTY).\n\n" +
			"A model that is already running is left running; its LiteLLM route is\n" +
			"written if it is missing. If omlx is still loading the model, wt waits for\n" +
			"the load.\n\n" +
			"On a provider that serves one model (mtplx), starting another replaces it.\n" +
			"On omlx a model loads beside the ones already loaded; when it does not fit,\n" +
			"omlx unloads the least recently used. wt asks before either; --replace skips\n" +
			"the question.",
		Example: "  wt start ollama/qwen3.8:27b-mlx\n  wt start",
		Args:    cobra.MaximumNArgs(1),
		// A refused or failed start is not a usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.cfgErr != nil {
				return configError(a.cfgErr)
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			replace, _ := cmd.Flags().GetBool("replace")
			return runStart(cmd.OutOrStdout(), a.cfg, a.theme, id, replace)
		},
	}
}

// servedProbeTimeout bounds each request `wt served` makes — the lifecycle
// engine's probe timeout, since it asks the same question.
const servedProbeTimeout = 5 * time.Second

// servedFamilies are the providers `wt served` answers for: the ones whose
// server says what it is serving. Ollama has no such answer — a pulled model
// loads on request — so it is left out rather than answered with its pulls.
var servedFamilies = []string{"mlx_lm_server", "mtplx", "omlx"}

func servedCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "served <provider>",
		Short: "List the models a local provider's server is serving now",
		Long: "List the model ids a local provider's server (" + strings.Join(servedFamilies, ", ") + ") is serving\n" +
			"now, one per line, as the server names them. This is the probe `wt start`,\n" +
			"`wt stop` and the pickers act on. For omlx it is the models loaded or loading,\n" +
			"not every model it lists; when the server has an API key, wt sends the one\n" +
			"the registry's omlx provider names (auth.secret_ref).\n\n" +
			"Nothing listed and exit 0 means the server answered and serves nothing.\n" +
			"Exit 1 means it gave no usable answer — not running, or it would not say.",
		Example: "  wt served omlx\n  wt served mtplx --json",
		Args:    cobra.ExactArgs(1),
		// A probe that gets no answer is the expected failure here, not a
		// usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The load error only: this reads the registry's provider rows
			// and writes nothing, so a validation gap elsewhere in the
			// config must not make the probe unavailable.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			client := &http.Client{Timeout: servedProbeTimeout}
			return runServed(cmd.OutOrStdout(), a.cfg, client, args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func runServed(out io.Writer, cfg *config.Config, client *http.Client, family string, asJSON bool) error {
	if !slices.Contains(servedFamilies, family) {
		return fmt.Errorf("unknown provider %q: wt served answers for %s", family, strings.Join(servedFamilies, ", "))
	}
	ids, err := localmodels.ServedIDs(cfg, client, family)
	if err != nil {
		return fmt.Errorf("%s gave no usable answer: %w", family, err)
	}
	if asJSON {
		if ids == nil {
			ids = []string{}
		}
		return json.NewEncoder(out).Encode(map[string]any{"provider": family, "served": ids})
	}
	for _, id := range ids {
		fmt.Fprintln(out, id)
	}
	return nil
}

func warmCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "warm <provider> <model>",
		Short: "Load a model into a running omlx server",
		Long: "Load a model into an omlx server that is already running, by sending it one\n" +
			"short request. <model> is the name omlx serves it under, or a repo id whose\n" +
			"last segment is that name. When the server has an API key, wt sends the one\n" +
			"the registry's omlx provider names (auth.secret_ref).\n\n" +
			"Nothing is started, stopped or routed: this is the warmup step of `wt start`\n" +
			"by itself, which llmbench's omlx backend asks for when omlx refuses its\n" +
			"keyless request. To start a model, use `wt start`.",
		Example: "  wt warm omlx Qwen3.8-27B-4bit",
		Args:    cobra.ExactArgs(2),
		// A server that refuses or never loads the model is the expected
		// failure here, not a usage mistake.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The load error only, as for `wt served`: this reads the
			// registry's provider rows and writes nothing.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			ctx, cancel := startSignalCtx()
			defer cancel()
			return runWarm(ctx, cmd.OutOrStdout(), a.cfg, args[0], args[1])
		},
	}
}

func runWarm(ctx context.Context, out io.Writer, cfg *config.Config, provider, model string) error {
	if localmodels.Family(provider) != "omlx" {
		return fmt.Errorf("unknown provider %q: wt warm loads models into omlx only", provider)
	}
	if err := lifecycle.Warm(ctx, cfg, provider, model); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is loaded on %s\n", model, provider)
	return nil
}

// localRows builds catalog rows for every local model the live inventory
// lists (registry models on disk or running, plus detected ones) from one
// snapshot — screen 1's row set.
func localRows(cfg *config.Config) []catalog.Row {
	rows, _ := localRowsSnap(cfg)
	return rows
}

// localRowsSnap is localRows plus the snapshot the rows were built from, for
// callers that must explain an id with no row (catalog.MissingReason).
func localRowsSnap(cfg *config.Config) ([]catalog.Row, localmodels.Snapshot) {
	snap := probeInventory(cfg)
	var models []config.Model
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err == nil && loc == config.LocationLocal {
			models = append(models, m)
		}
	}
	return catalog.Build(catalog.Input{Config: cfg, Models: models, Inventory: &snap}), snap
}

// runStart implements `wt start`. Argument errors return before any side effect.
func runStart(out io.Writer, cfg *config.Config, theme themes.Theme, id string, replace bool) error {
	rows, snap := localRowsSnap(cfg)
	if id == "" {
		if !stdinTTY() {
			return fmt.Errorf("wt start needs a TTY to list models; pass a model id directly (wt start <provider>/<name>)")
		}
		if len(rows) == 0 {
			return fmt.Errorf("no local model is on disk or running")
		}
		models := make([]config.Model, len(rows))
		for i, r := range rows {
			models[i] = r.Model
		}
		m, ok, err := pickStartModelTUI(cfg, models, theme)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("model selection canceled")
		}
		id = m.ID
	}
	row, ok := catalog.Find(rows, id)
	if !ok {
		if reason := catalog.MissingReason(cfg, &snap, id); reason != "" {
			return errors.New(reason)
		}
		if config.IndexModelByID(cfg.Models, id) >= 0 {
			return fmt.Errorf("%q is not a local model — wt start only starts local models", id)
		}
		return fmt.Errorf("unknown model %q", id)
	}
	switch row.Action() {
	case catalog.ActionStart:
		if err := startModel(cfg, row, replace); err != nil {
			return err
		}
		fmt.Fprintf(out, "wt: %s is running\n", id)
		return nil
	case catalog.ActionLaunch:
		// Already running, but not necessarily started by wt: write its route
		// if it is missing, removing nothing. Unconditional, whatever the
		// routing toggle says — the start hook writes its route the same way,
		// and `wt start` has no agent whose route could say whether the proxy
		// is on its path.
		ensureRouteBeforeLaunch(cfg, row.Model)
		fmt.Fprintf(out, "wt: %s is already running\n", id)
		return nil
	default: // catalog.ActionBlock: BlockReason is non-empty by definition
		return errors.New(row.BlockReason())
	}
}
