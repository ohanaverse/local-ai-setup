package lifecycle

import (
	"context"
	"errors"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// StopNotice is one line a stop gives while it waits, besides its outcome:
// that ollama is finishing a request in flight and the stop is waiting for
// it. Msg has no "wt: " prefix and no newline. It is true only while the stop
// waits, so a caller with a live screen shows it there and drops it once the
// stop returns; the stop loop prints it on the stop's open line.
type StopNotice struct {
	Msg string
}

type stopNoticeKey struct{}

// WithStopNotice returns a context that carries where a stop's notices go,
// the way litellm.WithRestartNotice carries the proxy restart's: the caller
// that prints the stop's line takes them, so they land on that line and never
// on a screen the caller does not own.
func WithStopNotice(ctx context.Context, fn func(StopNotice)) context.Context {
	return context.WithValue(ctx, stopNoticeKey{}, fn)
}

// stopNotice delivers n to the function ctx carries (WithStopNotice), or
// prints it as a route line (routePrintf: the operation's writer, else
// stderr) when it carries none.
func stopNotice(ctx context.Context, n StopNotice) {
	if fn, ok := ctx.Value(stopNoticeKey{}).(func(StopNotice)); ok && fn != nil {
		fn(n)
		return
	}
	routePrintf(ctx, "wt: %s\n", n.Msg)
}

// unloadAwaiter is a backend whose stop is accepted at once and carried out
// later (ollama): the stop is sent, then waited for. StopBatch sends every
// stop of a batch before it waits for any, so the waits overlap.
type unloadAwaiter interface {
	sendStop(ctx context.Context, e *env, cfg *config.Config, modelName string) stopSent
	awaitUnload(ctx context.Context, e *env, cfg *config.Config, modelName string, cliErr error, w *unloadWait) error
}

// stopKey names one model on one provider family.
type stopKey struct{ family, name string }

func keyOf(en localmodels.Entry) stopKey {
	return stopKey{localmodels.Family(en.ProviderID), en.ModelName}
}

// StopBatch is a batch of model stops (the stop loop's, internal/survey)
// that costs one wait however many ollama models it stops (#379).
// BeginStops sends `ollama stop` for every ollama model of the batch at once;
// each Stop then waits for its own model under one bound the whole batch
// shares, so three models each finishing a long request cost one bound, not
// three, before the loop reaches what comes after them (the omlx halt of `wt
// stop --all`, the exit flow's summary). Stops of other providers run when
// their Stop is called, in the caller's order. It is not safe for concurrent
// use.
type StopBatch struct {
	e    *env
	cfg  *config.Config
	wait *unloadWait
	sent map[stopKey]stopSent
	// order is the models whose stop was sent, in batch order; answered the
	// ones whose Stop has reported (gone, refused or still finishing), so
	// Accepted can name what ollama will still unload after a Ctrl+C.
	order    []localmodels.Entry
	answered map[stopKey]bool
}

// BeginStops sends the stop of every model of entries whose provider accepts
// a stop and carries it out later (ollama), in order, and returns the batch.
// The caller then calls Stop once per entry, in its own order. A cancelled
// ctx stops the sending; a model left unsent is sent by its Stop.
func BeginStops(ctx context.Context, cfg *config.Config, entries []localmodels.Entry) *StopBatch {
	return beginStops(ctx, defaultEnv(), cfg, entries)
}

// beginStops is BeginStops' injectable core.
func beginStops(ctx context.Context, e *env, cfg *config.Config, entries []localmodels.Entry) *StopBatch {
	b := &StopBatch{e: e, cfg: cfg, wait: newUnloadWait(e), sent: map[stopKey]stopSent{}, answered: map[stopKey]bool{}}
	for _, en := range entries {
		if ctx.Err() != nil {
			break
		}
		aw, ok := e.backends[localmodels.Family(en.ProviderID)].(unloadAwaiter)
		if !ok {
			continue
		}
		b.send(ctx, aw, en)
	}
	return b
}

// send sends en's stop once.
func (b *StopBatch) send(ctx context.Context, aw unloadAwaiter, en localmodels.Entry) stopSent {
	k := keyOf(en)
	if s, ok := b.sent[k]; ok {
		return s
	}
	s := aw.sendStop(ctx, b.e, b.cfg, en.ModelName)
	b.sent[k] = s
	b.order = append(b.order, en)
	return s
}

// Stop finishes the stop of one model of the batch and writes its route
// removal, as StopModelDeferred does: restartOwed reports that the caller
// owes SettleRoutes once after the batch. An ollama model is waited for under
// the batch's shared bound; one still listed once that bound is spent is a
// *StillFinishingError, which writes nothing and owes nothing (the model is
// still loaded).
func (b *StopBatch) Stop(ctx context.Context, en localmodels.Entry) (restartOwed bool, err error) {
	if err := b.stopOne(ctx, en); err != nil {
		return false, err
	}
	return routeRemoveModel(ctx, b.cfg, en, restartDeferred), nil
}

func (b *StopBatch) stopOne(ctx context.Context, en localmodels.Entry) error {
	be := b.e.backends[localmodels.Family(en.ProviderID)]
	if be == nil {
		return &UnsupportedError{ProviderID: en.ProviderID}
	}
	aw, ok := be.(unloadAwaiter)
	if !ok {
		return be.stopModel(ctx, b.e, b.cfg, en.ModelName)
	}
	s := b.send(ctx, aw, en)
	if s.fatal != nil {
		if ctx.Err() == nil {
			b.answered[keyOf(en)] = true
		}
		return s.fatal
	}
	err := aw.awaitUnload(ctx, b.e, b.cfg, en.ModelName, s.cliErr, b.wait)
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		b.answered[keyOf(en)] = true
	}
	return err
}

// Accepted lists, in batch order, the models whose stop ollama accepted and
// whose Stop has not reported: the ones a Ctrl+C left behind. ollama still
// unloads each once its request in flight ends, so a caller that stops early
// says so rather than leave them reading as untouched.
func (b *StopBatch) Accepted() []localmodels.Entry {
	var out []localmodels.Entry
	for _, en := range b.order {
		k := keyOf(en)
		if s := b.sent[k]; s.fatal == nil && s.cliErr == nil && !b.answered[k] {
			out = append(out, en)
		}
	}
	return out
}

// DeliverStopNoticeForTest delivers n as a stop's wait does, for a test in
// another package whose fake batch stands in for a StopBatch and must reach
// whatever the caller put on ctx (WithStopNotice). Production code never
// calls it.
func DeliverStopNoticeForTest(ctx context.Context, n StopNotice) { stopNotice(ctx, n) }
