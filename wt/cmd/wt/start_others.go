package main

import (
	"fmt"
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
// read by every start a session asks the model server for (`wt start`,
// `wt smoke`, a -M pin, a slot profile's local model, the picker). Like allowReplace it
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
