package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
)

// hostSessionReader is what `wt-model stop`'s stopSessions runs: the live
// server's client sessions per model, found through serve's runtime record
// (never through the session client, which this binary must not link).
//
// The returned func reads once and returns the same map on every call. No
// record (no server running, or no config home) is nil and silent; a record
// that cannot be read, or one whose server is not running (stale), is nil
// plus one note line on w, and the stop goes on without asking about any
// model. A record whose server is running but whose sessions cannot be read
// (a TLS verification failure, a refused token) is nil plus one note saying
// so, and the error is stored in *unread (when unread is not nil): the
// caller then asks or refuses rather than reading it as "no session". A read
// cut short by Ctrl+C (ctx cancelled) is nil and silent: the command then
// stops nothing (host.stopCancelled).
func hostSessionReader(ctx context.Context, w io.Writer, store servestate.Store, unread *error) func() map[string]int {
	return sessionReader(ctx, w, store, "models they use are stopped without asking", "wt asks before stopping any model", unread)
}

// aliasSessionReader is hostSessionReader for `wt-model alias list|set|rm`
// (the clientSessions seam), with its notes worded for them: no session
// count is known, so none is shown or checked, and an alias is not removed
// while a running server's sessions are unknown.
func aliasSessionReader(ctx context.Context, w io.Writer, store servestate.Store, unread *error) func() map[string]int {
	return sessionReader(ctx, w, store, "no alias is counted as in use", "an alias is not removed without --force-in-use", unread)
}

// sessionReader is the reader behind both: gone ends the note of a record
// that cannot be read or names a server that is not running, alive the note
// of a running server whose sessions cannot be read.
func sessionReader(ctx context.Context, w io.Writer, store servestate.Store, gone, alive string, unread *error) func() map[string]int {
	var (
		once sync.Once
		got  map[string]int
	)
	return func() map[string]int {
		once.Do(func() {
			rec, ok, err := store.Read()
			if err == nil && !ok {
				return
			}
			readErr := err != nil
			if err == nil {
				got, err = servestate.Sessions(ctx, rec)
			}
			if err != nil {
				got = nil
				// Ctrl+C: the command ends with nothing stopped
				// (stopCancelled), so "stopped without asking" would
				// be untrue.
				if ctx.Err() != nil {
					return
				}
				var stale *servestate.StaleError
				if readErr || errors.As(err, &stale) {
					fmt.Fprintf(w, "wt-model: note: could not read client sessions from the model server (%s) — %s\n", modelping.Clean(err.Error()), gone)
					return
				}
				// The server's process is alive: its sessions are unknown,
				// and the reason is said without the unreachable error's
				// "start it with" advice (it is running).
				if unread != nil {
					*unread = err
				}
				fmt.Fprintf(w, "wt-model: note: could not read client sessions from the model server (%s) — it is running (pid %d), so %s\n", sessionReadReason(err), rec.PID, alive)
			}
		})
		return got
	}
}

// sessionReadReason is why a running server's sessions could not be read:
// the transport reason of an unreachable error (whose own text advises
// starting a server that is running), else the error, one clean line.
func sessionReadReason(err error) string {
	var ue *modelping.UnreachableError
	if errors.As(err, &ue) && ue.Reason != "" {
		return modelping.Clean(ue.Reason)
	}
	return modelping.Clean(err.Error())
}
