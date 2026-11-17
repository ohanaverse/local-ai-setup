package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// cliAnswerer answers a model-server start job's questions on the command
// line (modelclient.Answerer): --replace and --others first, then the
// controlling terminal (P3: /dev/tty, never stdin), default No (P1). With no
// terminal the replace question is approval missing (P4, exit 3, naming
// --replace) and the others question keeps them with one warning line (the
// P4 exception the registry path already makes).
type cliAnswerer struct {
	id      string // the model being started, as the texts name it
	replace bool   // --replace
	others  string // --others: "", "keep" or "stop"
	// tty opens the terminal to ask on; an error means there is none.
	tty func() (*os.File, error)
	out io.Writer // warnings and the "replacing" line (stderr)
}

// noTerminalReplace is P4's refusal for the replace question: what the start
// would do, the flag that consents, and what did not happen.
func noTerminalReplace(what string) error {
	return exitcode.New(exitApprovalMissing,
		fmt.Errorf("approval missing: %s; pass --replace to go ahead (%s); nothing was started", what, errNoTerminal), false)
}

// Replace asks whether the start may stop what occupies the slot: the models
// the server names, or whatever an unreadable provider is serving.
func (a *cliAnswerer) Replace(ctx context.Context, p modelapi.Problem) (bool, error) {
	if a.replace {
		return true, nil
	}
	occupants := strings.Join(p.Occupants, ", ")
	what, question := p.Message, fmt.Sprintf("%s; replace it with %s?", p.Message, a.id)
	// An occupied problem with no occupants named has nothing to list: it keeps
	// the server's own message, as an unreadable provider's does.
	if p.Code == modelapi.CodeOccupied && len(p.Occupants) > 0 {
		what = fmt.Sprintf("starting %s would stop %s", a.id, occupants)
		question = fmt.Sprintf("starting %s will stop %s; continue?", a.id, occupants)
	}
	f, err := a.tty()
	if err != nil {
		return false, noTerminalReplace(what)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	ok, err := askOnTTY(ctx, f, "nothing was started", askYesNo)
	if ok && err == nil && p.Code == modelapi.CodeOccupied && len(p.Occupants) > 0 {
		fmt.Fprintf(a.out, "wt: replacing %s\n", occupants)
	}
	return ok, err
}

// Others asks what to do about other providers' models loaded beside the
// start: --others answers unasked; with no terminal they are kept and the
// warning is printed (once: the job driver asks this at most once a run).
func (a *cliAnswerer) Others(ctx context.Context, p modelapi.Problem) ([]string, bool, error) {
	switch a.others {
	case "stop":
		return modelapi.StoppableIDs(p.Others), true, nil
	case "keep":
		return nil, true, nil
	}
	f, err := a.tty()
	if err != nil {
		fmt.Fprint(a.out, othersWarning(a.id, p.Others, p.OthersUnknown))
		return nil, true, nil
	}
	defer f.Close()
	question, canStop := othersQuestion(a.id, p.Others, p.OthersUnknown, nil)
	if _, err := fmt.Fprintf(f, "%s ", question); err != nil {
		return nil, false, err
	}
	choice, err := askOnTTY(ctx, f, "nothing was started", func(r io.Reader) (othersChoice, error) { return askChoice(r, canStop) })
	if err != nil {
		return nil, false, err
	}
	switch choice {
	case othersStop:
		return modelapi.StoppableIDs(p.Others), true, nil
	case othersKeep:
		return nil, true, nil
	}
	return nil, false, nil
}

// ConfirmInUse asks whether to stop a model other client sessions use.
// Without a terminal it is approval missing, naming --yes.
func (a *cliAnswerer) ConfirmInUse(ctx context.Context, p modelapi.Problem) (bool, error) {
	f, err := a.tty()
	if err != nil {
		return false, exitcode.New(exitApprovalMissing,
			fmt.Errorf("approval missing: %s; pass --yes to go ahead (%s); nothing was stopped", p.Message, errNoTerminal), false)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s; stop it anyway? [y/N] ", p.Message); err != nil {
		return false, err
	}
	return askOnTTY(ctx, f, "nothing was stopped", askYesNo)
}

// cancelledQuestion is a prompt Ctrl-C ended: M7 words a cancellation
// "cancelled — <what is still true>", never "declined" (that stays the
// answered no's word), with a decline's outcome (C9 exit 3, nothing changed).
func cancelledQuestion(still string) error {
	return exitcode.New(exitApprovalMissing, errors.New("cancelled — "+still), false)
}

// askOnTTY reads an answer with read, raced against ctx: the read on the
// terminal cannot be interrupted, and Ctrl-C (which the start's signal
// context swallows) must still end the question. A cancelled question is not
// a decline: it answers cancelledQuestion(still), with still what the
// question guarded ("nothing was started", "nothing was stopped"). The
// abandoned read dies with the process.
func askOnTTY[T any](ctx context.Context, f io.Reader, still string, read func(io.Reader) (T, error)) (T, error) {
	type answer struct {
		v   T
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		v, err := read(f)
		ch <- answer{v, err}
	}()
	select {
	case a := <-ch:
		return a.v, a.err
	case <-ctx.Done():
		var zero T
		return zero, cancelledQuestion(still)
	}
}

// askChoice reads one line from r: s/stop stops (only when canStop), k/keep
// keeps, and anything else, an empty line and EOF included, cancels. Split
// out of the others question so tests can drive it.
func askChoice(r io.Reader, canStop bool) (othersChoice, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return othersCancel, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "s", "stop":
		if canStop {
			return othersStop, nil
		}
	case "k", "keep":
		return othersKeep, nil
	}
	return othersCancel, nil
}

// othersQuestion is the others prompt for a start of id: one line per
// resident with its memory figure and, when counts names wt sessions using
// it, how many (nil on the model server's path); one
// line per provider wt cannot read (unknown, already worded: "omlx at URL" on
// the registry path, the family on the server's); then the choice, without s
// when nothing listed can be stopped.
func othersQuestion(id string, others []modelapi.Resident, unknown []string, counts map[string]int) (string, bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "starting %s loads it beside models of other providers:\n", id)
	for _, r := range others {
		line := fmt.Sprintf("%s — %s", r.Label, r.Figure)
		fmt.Fprintf(&b, "  %s\n", lifecycle.WithSessions(line, counts[r.ID]))
	}
	for _, f := range unknown {
		fmt.Fprintf(&b, "  cannot tell whether %s is serving a model\n", f)
	}
	canStop := len(modelapi.StoppableIDs(others)) > 0
	if canStop {
		b.WriteString("stop them first, keep them and start, or cancel? [s/k/N]")
	} else {
		b.WriteString("keep them and start, or cancel? [k/N]")
	}
	return b.String(), canStop
}

// othersWarning is the one line a start without a terminal and without
// --others prints when it keeps other providers' models. It offers
// --others=stop only when something it names can be stopped: with every
// resident unstoppable (an mlx_lm_server pairing) or unreadable, that flag
// would stop nothing, and a script told to pass it would believe it had made
// room.
func othersWarning(id string, others []modelapi.Resident, unknown []string) string {
	var parts []string
	for _, r := range others {
		parts = append(parts, fmt.Sprintf("%s (%s)", r.Label, r.Figure))
	}
	for _, f := range unknown {
		parts = append(parts, fmt.Sprintf("whatever %s is serving", f))
	}
	advice := "pass --others=keep to silence this"
	if len(modelapi.StoppableIDs(others)) > 0 {
		advice = "pass --others=stop to stop them, --others=keep to silence this"
	}
	return fmt.Sprintf("wt: warning: starting %s beside %s; %s\n", id, strings.Join(parts, ", "), advice)
}

// startProgressRemote is a start job's progress on out (stderr, M5): one
// `wt: starting ID — STEP (12s)` line when the step changes and again every
// startProgressInterval on the client's clock, never one per poll. The
// server's step is a remote party's text, flattened to one printable line
// (M11) here as well as in modelclient.
func startProgressRemote(out io.Writer, id string) modelclient.Progress {
	return jobProgressRemote(out, "starting", id)
}

// jobProgressRemote is startProgressRemote for any job: verb is the line's
// first word ("starting", "stopping").
func jobProgressRemote(out io.Writer, verb, id string) modelclient.Progress {
	id = cleanup.Flatten(id)
	said := false
	var last string
	var lastAt time.Duration
	return func(v modelclient.JobView) {
		detail := cleanup.Flatten(v.Step)
		switch {
		case v.Queued:
			detail = modelclient.QueuedDetail
		case detail == "":
			detail = "waiting for the model server"
		}
		if said && detail == last && v.Elapsed-lastAt < startProgressInterval {
			return
		}
		said, last, lastAt = true, detail, v.Elapsed
		fmt.Fprintf(out, "wt: %s %s — %s (%s)\n", verb, id, detail, v.Elapsed.Round(time.Second))
	}
}

// setModel names the model the answerer's texts speak of: `wt start` with no
// id learns it from the picker, after the answerer was built.
func (a *cliAnswerer) setModel(id string) { a.id = id }
