package lifecycle

import (
	"errors"
	"fmt"
)

// StageLabel is the user-facing phrase for a start stage, shared by the TUI's
// start screen and the non-TUI start driver so the two cannot drift.
func StageLabel(s Stage) string {
	switch s {
	case StageStoppingOccupant:
		return "stopping the running model"
	case StageStarting:
		return "starting the server"
	case StageWaiting:
		return "waiting for the model to load"
	case StageWarming:
		return "warming the model"
	case StageRouting:
		return "updating LiteLLM routes"
	}
	return "starting"
}

// StartErrorMessage is the one-line message for a failed start, shared by the
// TUI's picker status line and the non-TUI driver's stderr. A down daemon
// names the provider and origin (the user has to start an app or service);
// the binary and port errors are already actionable as written; everything
// else is prefixed with the model id so an unattributed engine error is still
// traceable to the row it came from. The replace-confirmation errors
// (OccupiedError, OccupancyUnknownError) are deliberately unmapped — callers
// handle those interactively, not as a failure line.
func StartErrorMessage(id string, err error) string {
	var down *DaemonDownError
	var bin *BinaryMissingError
	var busy *PortBusyError
	switch {
	case errors.As(err, &down):
		return fmt.Sprintf("%s is not answering at %s — start it first", down.Provider, down.Origin)
	case errors.As(err, &bin), errors.As(err, &busy):
		return err.Error()
	}
	return fmt.Sprintf("failed to start %s: %v", id, err)
}
