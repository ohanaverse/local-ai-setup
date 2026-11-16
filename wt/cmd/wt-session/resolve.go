package main

import (
	"fmt"
)

// allowReplace is the process-wide --replace mode: set once from the root
// command's flag and read by the launch paths (launchFilteredRemote, the
// picker, a slot profile's start). It is CLI mode state, not per-launch state.
var allowReplace bool

// errCommandAgent is the sentinel returned by resolveModelRemote when the
// resolved agent is a command (no model layer). Callers skip the model
// step and launch the command directly.
var errCommandAgent = fmt.Errorf("agent is a command")
