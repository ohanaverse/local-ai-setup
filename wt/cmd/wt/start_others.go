package main

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
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

// legacyOthers is a lifecycle refusal's others in the form the prompt helpers
// (answerer.go) take: each resident as the wire carries one (its label and
// memory figure), and each family wt cannot read named with its address, as
// the registry path has always worded it.
func legacyOthers(cfg *config.Config, occ *lifecycle.OccupiedError) ([]modelapi.Resident, []string) {
	others := make([]modelapi.Resident, len(occ.Others))
	for i, r := range occ.Others {
		others[i] = modelapi.Resident{ID: r.Entry.ModelID, Label: r.Label(), Stoppable: r.Stoppable, Figure: r.Figure()}
	}
	unknown := make([]string, len(occ.OthersUnknown))
	for i, f := range occ.OthersUnknown {
		unknown[i] = fmt.Sprintf("%s at %s", f, localmodels.FamilyOrigin(cfg, f))
	}
	return others, unknown
}

// legacyOthersCounts is how many wt sessions use each other resident, from
// the refcount file (the registry path only).
func legacyOthersCounts(occ *lifecycle.OccupiedError) map[string]int {
	ids := make([]string, len(occ.Others))
	for i, r := range occ.Others {
		ids[i] = r.Entry.ModelID
	}
	if len(ids) == 0 {
		return map[string]int{}
	}
	return sessionCounts(ids)
}

// othersCancelled is the error of a declined others question: what is still
// loaded, by id.
func othersCancelled(occ *lifecycle.OccupiedError) error {
	ids := make([]string, len(occ.Others))
	for i, r := range occ.Others {
		ids[i] = r.Entry.ModelID
	}
	if len(ids) == 0 {
		return declined("nothing was stopped")
	}
	return declined(strings.Join(ids, ", ") + " still loaded")
}
