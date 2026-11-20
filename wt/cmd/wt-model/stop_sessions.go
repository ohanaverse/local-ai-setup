package main

import (
	"context"
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
// that cannot be read, or a server that cannot be asked, is nil plus one note
// line on w, so the stop goes on without asking about any model.
func hostSessionReader(ctx context.Context, w io.Writer, store servestate.Store) func() map[string]int {
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
			if err == nil {
				got, err = servestate.Sessions(ctx, rec)
			}
			if err != nil {
				got = nil
				fmt.Fprintf(w, "wt-model: note: could not read client sessions from the model server (%s) — models they use are stopped without asking\n", modelping.Clean(err.Error()))
			}
		})
		return got
	}
}
