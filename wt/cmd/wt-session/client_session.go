package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// openClientSession registers a client session for ids with the model server
// and starts its heartbeat: the one place the lifecycle rule lives, for every
// command that holds a model from before its start to its end (launch, slot
// launch, `wt start`, `wt smoke`).
//
// A register failure is a note, never an error: whenFailed says what goes on
// anyway ("the launch goes on, but the server may stop X once it is idle").
// note writes one "wt: note:" line (the heartbeat goroutine calls it too).
// end ends the session once, on a context no cancel reaches, and never fails
// the caller; it is safe to call more than once (a defer plus an early call).
// It says nothing after a register no retry can heal (the DELETE is refused
// too), and notes a failed End otherwise.
func openClientSession(ctx context.Context, ms *modelSession, ids []string, whenFailed string) (sess *modelclient.Session, note func(string), end func()) {
	note = func(s string) { fmt.Fprintf(osStderr, "wt: note: %s\n", s) }
	sess = ms.client.NewSession(ids, modelclient.SessionOptions{Interval: remoteBeatInterval, Note: note})
	registered := true
	if rerr := sess.Start(ctx); rerr != nil {
		registered = !registerRefused(rerr)
		note(fmt.Sprintf("the model server did not register this client session (%s) — %s", rerr, whenFailed))
	}
	var once sync.Once
	end = func() {
		once.Do(func() {
			if eerr := sess.End(context.WithoutCancel(ctx)); eerr != nil && registered {
				note(fmt.Sprintf("could not end the client session on the model server (%s) — it forgets it within a minute", eerr))
			}
		})
	}
	return sess, note, end
}
