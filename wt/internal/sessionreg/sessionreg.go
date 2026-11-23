// Package sessionreg is a pure in-memory registry of which sessions use which
// models. Session ids are client-supplied strings; a session is live while its
// last heartbeat is within the TTL, evaluated at read time against an injected
// clock, so a crashed client simply stops counting.
//
// A session may name an alias (#596) instead of a model. With an expansion
// set (SetExpand), a session on an alias also uses the alias's current local
// target(s): the reads resolve that at read time, never at Register, so a
// re-point of the alias is seen without the session registering again.
package sessionreg

import (
	"errors"
	"sort"
	"sync"
	"time"
)

type session struct {
	models   map[string]struct{}
	lastBeat time.Time
}

// Registry tracks sessions and the models they use. Safe for concurrent use.
// A session whose TTL lapsed is deleted from the map by the read paths that
// iterate it (Counts, Live, Users), so sessions whose client crashed without
// an End do not accumulate; it stays non-counting for that decision exactly
// as a merely lapsed one would.
type Registry struct {
	ttl time.Duration
	now func() time.Time

	expMu  sync.Mutex
	expand func() map[string][]string

	mu       sync.Mutex
	sessions map[string]*session
	lastUsed map[string]time.Time
}

// New returns an empty registry; now supplies the clock.
func New(ttl time.Duration, now func() time.Time) *Registry {
	return &Registry{
		ttl:      ttl,
		now:      now,
		sessions: map[string]*session{},
		lastUsed: map[string]time.Time{},
	}
}

// SetExpand sets the alias expansion: f returns, for each alias id, its
// current local target id(s). Counts, Live, Users, Seen, IdleSince, Touch and
// Forget call f once per call, before the registry's lock is taken (f may
// reload the registry), and treat a session on an alias as also using its
// targets. nil (the default) expands nothing.
func (r *Registry) SetExpand(f func() map[string][]string) {
	r.expMu.Lock()
	defer r.expMu.Unlock()
	r.expand = f
}

// expansion is one read's snapshot of the alias expansion: fwd maps an alias
// to its targets, inv a target to the aliases that expand to it.
type expansion struct {
	fwd map[string][]string
	inv map[string][]string
}

// snapshot takes the expansion once. It must be called without r.mu held.
func (r *Registry) snapshot() expansion {
	r.expMu.Lock()
	f := r.expand
	r.expMu.Unlock()
	if f == nil {
		return expansion{}
	}
	x := expansion{fwd: map[string][]string{}, inv: map[string][]string{}}
	for alias, targets := range f() {
		for _, t := range targets {
			if t == "" || t == alias {
				continue
			}
			x.fwd[alias] = append(x.fwd[alias], t)
			x.inv[t] = append(x.inv[t], alias)
		}
	}
	return x
}

// uses is every model s uses: its own and the targets its aliases expand to,
// each once.
func (x expansion) uses(s *session) map[string]struct{} {
	if len(x.fwd) == 0 {
		return s.models
	}
	out := make(map[string]struct{}, len(s.models))
	for m := range s.models {
		out[m] = struct{}{}
		for _, t := range x.fwd[m] {
			out[t] = struct{}{}
		}
	}
	return out
}

func (r *Registry) live(s *session, at time.Time) bool {
	return at.Sub(s.lastBeat) <= r.ttl
}

// Register creates or replaces a session's model set and refreshes its beat.
// Empty and duplicate model ids are dropped.
func (r *Registry) Register(id string, models []string) error {
	if id == "" {
		return errors.New("the client session id is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	s := &session{models: map[string]struct{}{}, lastBeat: at}
	for _, m := range models {
		if m == "" {
			continue
		}
		s.models[m] = struct{}{}
		r.lastUsed[m] = at
	}
	r.sessions[id] = s
	return nil
}

// Heartbeat adds models to a live session and refreshes it. It reports false
// for an unknown or lapsed session, which is not resurrected.
func (r *Registry) Heartbeat(id string, models []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	s, ok := r.sessions[id]
	if !ok || !r.live(s, at) {
		return false
	}
	s.lastBeat = at
	for _, m := range models {
		if m == "" {
			continue
		}
		s.models[m] = struct{}{}
	}
	for m := range s.models {
		r.lastUsed[m] = at
	}
	return true
}

// End removes a session and starts the idle clock for its models. A session
// its TTL already expired does not restart the clock at the end: a client
// deleting a session record long after it stopped heartbeating must not
// postpone the auto-stop of models that have been idle all that time. Their
// lastUsed already reads the session's last beat.
func (r *Registry) End(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return
	}
	at := r.now()
	if r.live(s, at) {
		for m := range s.models {
			r.lastUsed[m] = at
		}
	}
	delete(r.sessions, id)
}

// pruneLocked deletes every lapsed session and, for each live session on an
// alias, moves the idle clock of the alias's current targets up to the
// session's last beat (registering them): a target a session uses through an
// alias is in use exactly as one it names, so after a re-point the old target
// keeps a clock and auto-stop stops it after the grace instead of leaking it.
// It must be called with r.mu held, and every read path calls it first.
func (r *Registry) pruneLocked(at time.Time, x expansion) {
	for id, s := range r.sessions {
		if !r.live(s, at) {
			delete(r.sessions, id)
			continue
		}
		for m := range s.models {
			for _, t := range x.fwd[m] {
				if cur, ok := r.lastUsed[t]; !ok || cur.Before(s.lastBeat) {
					r.lastUsed[t] = s.lastBeat
				}
			}
		}
	}
}

// Counts returns the number of live sessions per requested model, zero-filled.
// A session on an alias also counts for the alias's targets, once.
func (r *Registry) Counts(ids []string) map[string]int {
	out := make(map[string]int, len(ids))
	for _, id := range ids {
		out[id] = 0
	}
	x := r.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	r.pruneLocked(at, x)
	for _, s := range r.sessions {
		for m := range x.uses(s) {
			if _, want := out[m]; want {
				out[m]++
			}
		}
	}
	return out
}

// Live returns every model with at least one live session, an alias's
// targets included.
func (r *Registry) Live() map[string]int {
	out := map[string]int{}
	x := r.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now(), x)
	for _, s := range r.sessions {
		for m := range x.uses(s) {
			out[m]++
		}
	}
	return out
}

// Users returns the sorted ids of live sessions using model, directly or
// through an alias that expands to it.
func (r *Registry) Users(model string) []string {
	var out []string
	x := r.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now(), x)
	for id, s := range r.sessions {
		if _, ok := x.uses(s)[model]; ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// IdleSince returns the later of the last beat or the end of a session that
// was still live when it ended (see End), taking for a target the latest
// clock of itself and of every alias that expands to it; seen is false when
// no session ever registered it (or an alias of it). The time is meaningful
// only while the model's live count is 0.
func (r *Registry) IdleSince(model string) (time.Time, bool) {
	x := r.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now(), x)
	t, ok := r.lastUsed[model]
	for _, a := range x.inv[model] {
		if at, seen := r.lastUsed[a]; seen && (!ok || at.After(t)) {
			t, ok = at, true
		}
	}
	return t, ok
}

// Seen returns every model registered and not since forgotten (Forget), with
// the current targets of every such alias, sorted.
func (r *Registry) Seen() []string {
	x := r.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now(), x)
	set := make(map[string]struct{}, len(r.lastUsed))
	for m := range r.lastUsed {
		set[m] = struct{}{}
		for _, t := range x.fwd[m] {
			set[t] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Touch restarts the idle clock of a model some session has registered, and
// for an alias also sets its targets' clocks (registering them). It does
// nothing for a model never registered, which therefore stays out of
// auto-stop's reach.
func (r *Registry) Touch(model string) {
	x := r.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.lastUsed[model]; !ok {
		return
	}
	at := r.now()
	r.lastUsed[model] = at
	for _, t := range x.fwd[model] {
		r.lastUsed[t] = at
	}
}

// Forget drops a model from Seen (and its idle clock), so auto-stop leaves it
// alone until some session registers it again; Touch does not bring it back.
// A model a live session lists is kept: it is that session's model, and so is
// a target a live session's alias expands to. Forgetting an alias first
// carries its last-used time to each of its targets whose clock is earlier or
// absent, so the target stays in auto-stop's reach for the grace.
func (r *Registry) Forget(model string) {
	x := r.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	r.pruneLocked(at, x)
	for _, s := range r.sessions {
		if _, ok := x.uses(s)[model]; ok {
			return
		}
	}
	if used, ok := r.lastUsed[model]; ok {
		for _, t := range x.fwd[model] {
			if cur, seen := r.lastUsed[t]; !seen || cur.Before(used) {
				r.lastUsed[t] = used
			}
		}
	}
	delete(r.lastUsed, model)
}
