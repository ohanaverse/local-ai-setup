// Package sessionreg is a pure in-memory registry of which sessions use which
// models. Session ids are client-supplied strings; a session is live while its
// last heartbeat is within the TTL, evaluated at read time against an injected
// clock, so a crashed client simply stops counting.
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

func (r *Registry) live(s *session, at time.Time) bool {
	return at.Sub(s.lastBeat) <= r.ttl
}

// Register creates or replaces a session's model set and refreshes its beat.
// Empty and duplicate model ids are dropped.
func (r *Registry) Register(id string, models []string) error {
	if id == "" {
		return errors.New("sessionreg: empty session id")
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

// pruneLocked deletes every lapsed session. It must be called with r.mu held
// and read paths that iterate the sessions call it first, so the map holds at
// most the live sessions plus recently lapsed ones no one has read since.
func (r *Registry) pruneLocked(at time.Time) {
	for id, s := range r.sessions {
		if !r.live(s, at) {
			delete(r.sessions, id)
		}
	}
}

// Counts returns the number of live sessions per requested model, zero-filled.
func (r *Registry) Counts(ids []string) map[string]int {
	out := make(map[string]int, len(ids))
	for _, id := range ids {
		out[id] = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	r.pruneLocked(at)
	for _, s := range r.sessions {
		if !r.live(s, at) {
			continue
		}
		for m := range s.models {
			if _, want := out[m]; want {
				out[m]++
			}
		}
	}
	return out
}

// Live returns every model with at least one live session.
func (r *Registry) Live() map[string]int {
	out := map[string]int{}
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	r.pruneLocked(at)
	for _, s := range r.sessions {
		if !r.live(s, at) {
			continue
		}
		for m := range s.models {
			out[m]++
		}
	}
	return out
}

// Users returns the sorted ids of live sessions using model.
func (r *Registry) Users(model string) []string {
	var out []string
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now()
	r.pruneLocked(at)
	for id, s := range r.sessions {
		if _, ok := s.models[model]; ok && r.live(s, at) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// IdleSince returns the later of the last beat or the end of a session that
// was still live when it ended (see End); seen is false when no session ever
// registered it. The time is meaningful only while the model's live count
// is 0.
func (r *Registry) IdleSince(model string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.lastUsed[model]
	return t, ok
}

// Seen returns every model ever registered, sorted.
func (r *Registry) Seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.lastUsed))
	for m := range r.lastUsed {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Touch restarts the idle clock of a model some session has registered. It
// does nothing for a model never registered, which therefore stays out of
// auto-stop's reach.
func (r *Registry) Touch(model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.lastUsed[model]; ok {
		r.lastUsed[model] = r.now()
	}
}
