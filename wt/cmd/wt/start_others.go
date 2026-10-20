package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// othersFlag is the value of the root --others flag (#412): "" asks about
// other providers' models loaded beside the model a start loads (or, without
// a TTY, keeps them and warns), "keep" keeps them unasked, "stop" stops every
// one wt can stop. Set refuses anything else, so a typo is a usage error
// before anything runs rather than a silent ask.
type othersFlag string

func (f *othersFlag) String() string { return string(*f) }

func (f *othersFlag) Set(v string) error {
	switch v {
	case "keep", "stop":
		*f = othersFlag(v)
		return nil
	}
	return fmt.Errorf("want keep or stop")
}

func (f *othersFlag) Type() string { return "keep|stop" }

// othersMode is the process-wide --others answer, set by flag parsing and
// read by driveStart, which every non-TUI start goes through (`wt start`,
// `wt smoke`, a -M pin, a slot profile's local model). Like allowReplace it
// is CLI mode state, not per-launch state. rootCmd resets it.
var othersMode othersFlag

// othersChoice is an answer to the others question.
type othersChoice int

const (
	othersCancel othersChoice = iota
	othersKeep
	othersStop
)

func (c othersChoice) String() string {
	switch c {
	case othersKeep:
		return "keep"
	case othersStop:
		return "stop"
	}
	return "cancel"
}

// chooseOthers is a test seam over the others prompt on /dev/tty.
var chooseOthers = promptOthers

// promptOthers asks the others question on the controlling terminal, never
// on stdin and stdout, for the reason promptReplace gives: a piped answer
// must never be able to stop a model. question ends in its own [s/k/N] or
// [k/N]; canStop says whether s is an answer.
func promptOthers(question string, canStop bool) (othersChoice, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return othersCancel, fmt.Errorf("cannot ask about other providers' models: %w — rerun with --others=keep or --others=stop", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s ", question); err != nil {
		return othersCancel, err
	}
	return askChoice(f, canStop)
}

// askChoice reads one line from r: s/stop stops (only when canStop), k/keep
// keeps, and anything else, an empty line and EOF included, cancels. The
// parse half of promptOthers, split out so tests can drive it.
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

// stoppableIDs are the model ids of the residents wt can stop, in the
// engine's order: what a stop answer approves.
func stoppableIDs(others []lifecycle.Resident) []string {
	var ids []string
	for _, r := range others {
		if r.Stoppable {
			ids = append(ids, r.Entry.ModelID)
		}
	}
	return ids
}

// othersQuestion is the others prompt for a start of id: one line per
// resident with its figure and, when wt sessions use it, how many; one line
// per family wt cannot read; then the choice, without s when nothing listed
// can be stopped.
func othersQuestion(cfg *config.Config, id string, occ *lifecycle.OccupiedError) (string, bool) {
	ids := make([]string, len(occ.Others))
	for i, r := range occ.Others {
		ids[i] = r.Entry.ModelID
	}
	counts := map[string]int{}
	if len(ids) > 0 {
		counts = sessionCounts(ids)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "starting %s loads it beside models of other providers:\n", id)
	for _, r := range occ.Others {
		fmt.Fprintf(&b, "  %s — %s", r.Label(), r.Figure())
		if n := counts[r.Entry.ModelID]; n > 0 {
			fmt.Fprintf(&b, " (in use by %d wt session(s))", n)
		}
		b.WriteString("\n")
	}
	for _, f := range occ.OthersUnknown {
		fmt.Fprintf(&b, "  cannot tell whether %s at %s is serving a model\n", f, localmodels.FamilyOrigin(cfg, f))
	}
	canStop := len(stoppableIDs(occ.Others)) > 0
	if canStop {
		b.WriteString("stop them first, keep them and start, or cancel? [s/k/N]")
	} else {
		b.WriteString("keep them and start, or cancel? [k/N]")
	}
	return b.String(), canStop
}

// othersWarning is the one line a start without a TTY and without --others
// prints when it keeps other providers' models.
func othersWarning(cfg *config.Config, id string, occ *lifecycle.OccupiedError) string {
	var parts []string
	for _, r := range occ.Others {
		parts = append(parts, fmt.Sprintf("%s (%s)", r.Label(), r.Figure()))
	}
	for _, f := range occ.OthersUnknown {
		parts = append(parts, fmt.Sprintf("whatever %s at %s is serving", f, localmodels.FamilyOrigin(cfg, f)))
	}
	return fmt.Sprintf("wt: warning: starting %s beside %s; pass --others=stop to stop them, --others=keep to silence this\n",
		id, strings.Join(parts, ", "))
}

// othersCancelled is the error of a declined others question: what is still
// loaded, by id.
func othersCancelled(occ *lifecycle.OccupiedError) error {
	ids := make([]string, len(occ.Others))
	for i, r := range occ.Others {
		ids[i] = r.Entry.ModelID
	}
	if len(ids) == 0 {
		return fmt.Errorf("cancelled — nothing was stopped")
	}
	return fmt.Errorf("cancelled — %s still loaded", strings.Join(ids, ", "))
}
