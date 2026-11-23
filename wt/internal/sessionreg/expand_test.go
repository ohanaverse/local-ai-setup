package sessionreg

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// expander is a settable alias expansion that counts its calls.
type expander struct {
	mu    sync.Mutex
	m     map[string][]string
	calls int
}

func (e *expander) get() map[string][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return e.m
}

func (e *expander) set(m map[string][]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.m = m
}

func (e *expander) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// TestExpandIsCalledOncePerRead checks that every read that honors the alias
// expansion (Counts, Live, Users, Seen, IdleSince, Touch, Forget) takes the
// snapshot exactly once per call, and that the expansion is called without
// the registry lock held (it may reload the registry, and a call under r.mu
// would make every session request wait on that I/O): the expansion itself
// calls into the registry, which deadlocks if r.mu is held.
func TestExpandIsCalledOncePerRead(t *testing.T) {
	r, _ := newReg(time.Minute)
	e := &expander{m: map[string][]string{"alias/x": {"omlx/a"}}}
	r.SetExpand(e.get)
	_ = r.Register("s", []string{"alias/x"})
	reads := map[string]func(){
		"Counts":    func() { r.Counts([]string{"omlx/a"}) },
		"Live":      func() { r.Live() },
		"Users":     func() { r.Users("omlx/a") },
		"Seen":      func() { r.Seen() },
		"IdleSince": func() { r.IdleSince("omlx/a") },
		"Touch":     func() { r.Touch("alias/x") },
		"Forget":    func() { r.Forget("omlx/b") },
	}
	for name, read := range reads {
		before := e.count()
		read()
		if got := e.count() - before; got != 1 {
			t.Errorf("%s called the expansion %d times, want 1", name, got)
		}
	}
	// An expansion that reads the registry must not deadlock.
	done := make(chan struct{})
	r.SetExpand(func() map[string][]string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return map[string][]string{"alias/x": {"omlx/a"}}
	})
	go func() {
		r.Live()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Live called the expansion with r.mu held (deadlock)")
	}
}

// TestAliasSessionCountsAsItsTargetsUse checks that a live session on an
// alias counts as a user of the alias's current local target in Counts, Live
// and Users, once per session even when it also lists the target, and that a
// re-point is seen at the next read without the session registering again:
// auto-stop asks these reads whether a local model is in use.
func TestAliasSessionCountsAsItsTargetsUse(t *testing.T) {
	r, _ := newReg(time.Minute)
	e := &expander{m: map[string][]string{"alias/x": {"omlx/a"}}}
	r.SetExpand(e.get)
	_ = r.Register("s1", []string{"alias/x"})
	_ = r.Register("s2", []string{"alias/x", "omlx/a"})
	if got := r.Counts([]string{"omlx/a", "alias/x", "omlx/b"}); got["omlx/a"] != 2 || got["alias/x"] != 2 || got["omlx/b"] != 0 {
		t.Errorf("counts = %v, want omlx/a:2 alias/x:2 omlx/b:0", got)
	}
	if got := r.Live(); got["omlx/a"] != 2 || got["alias/x"] != 2 {
		t.Errorf("live = %v", got)
	}
	if got := r.Users("omlx/a"); !reflect.DeepEqual(got, []string{"s1", "s2"}) {
		t.Errorf("users of omlx/a = %v", got)
	}
	e.set(map[string][]string{"alias/x": {"omlx/b"}}) // re-point
	if got := r.Users("omlx/b"); !reflect.DeepEqual(got, []string{"s1", "s2"}) {
		t.Errorf("users of omlx/b after the re-point = %v", got)
	}
	if got := r.Users("omlx/a"); !reflect.DeepEqual(got, []string{"s2"}) {
		t.Errorf("users of omlx/a after the re-point = %v, want only the direct user", got)
	}
}

// TestAliasSessionMakesItsTargetSeen checks that a target an alias session
// uses is in Seen with an idle clock: while the session is live (so a
// re-point leaves the old target with a clock and auto-stop stops it after
// the grace instead of leaking it), and after the session ends (the alias's
// end time).
func TestAliasSessionMakesItsTargetSeen(t *testing.T) {
	r, c := newReg(time.Minute)
	e := &expander{m: map[string][]string{"alias/x": {"omlx/a"}}}
	r.SetExpand(e.get)
	_ = r.Register("s", []string{"alias/x"})
	if got := r.Seen(); !reflect.DeepEqual(got, []string{"alias/x", "omlx/a"}) {
		t.Errorf("seen = %v", got)
	}
	start := c.t
	c.t = c.t.Add(30 * time.Second)
	_ = r.Heartbeat("s", nil)
	e.set(map[string][]string{"alias/x": {"omlx/b"}})
	if at, ok := r.IdleSince("omlx/a"); !ok || at.Before(start) {
		t.Errorf("old target idle since %v %v, want a clock from its use", at, ok)
	}
	c.t = c.t.Add(10 * time.Second)
	r.End("s")
	end := c.t
	if at, ok := r.IdleSince("omlx/b"); !ok || !at.Equal(end) {
		t.Errorf("new target idle since %v %v, want the session's end %v", at, ok, end)
	}
}

// TestTouchOfAnAliasTouchesItsTarget checks that Touch of a registered alias
// also sets its target's idle clock, registering the target, so a model an
// alias start just started is not auto-stopped on the next scan.
func TestTouchOfAnAliasTouchesItsTarget(t *testing.T) {
	r, c := newReg(time.Minute)
	r.SetExpand((&expander{m: map[string][]string{"alias/x": {"omlx/a"}}}).get)
	_ = r.Register("s", []string{"alias/x"})
	r.End("s")
	r.Forget("omlx/a")
	c.t = c.t.Add(time.Hour)
	r.Touch("alias/x")
	if at, ok := r.IdleSince("omlx/a"); !ok || !at.Equal(c.t) {
		t.Errorf("target idle since %v %v, want now %v", at, ok, c.t)
	}
}

// TestForgetKeepsATargetAnAliasSessionUses checks the two Forget rules for
// aliases: a target is not forgotten while a live session's alias expands to
// it, and forgetting an alias first carries its last-used time to its target
// (when that is later than the target's own clock or the target has none),
// so the target stays in auto-stop's reach and is stopped after the grace
// rather than leaked.
func TestForgetKeepsATargetAnAliasSessionUses(t *testing.T) {
	r, c := newReg(time.Minute)
	r.SetExpand((&expander{m: map[string][]string{"alias/x": {"omlx/a"}}}).get)
	_ = r.Register("s", []string{"alias/x"})
	r.Forget("omlx/a")
	if _, ok := r.IdleSince("omlx/a"); !ok {
		t.Fatal("Forget dropped a target a live alias session uses")
	}
	r.Forget("alias/x")
	if _, ok := r.IdleSince("alias/x"); !ok {
		t.Fatal("Forget dropped an alias a live session lists")
	}
	c.t = c.t.Add(20 * time.Second)
	r.End("s")
	end := c.t
	r.Forget("alias/x")
	if got := r.Seen(); !reflect.DeepEqual(got, []string{"omlx/a"}) {
		t.Fatalf("seen after forgetting the alias = %v, want only its target", got)
	}
	if at, _ := r.IdleSince("omlx/a"); !at.Equal(end) {
		t.Errorf("target idle since %v, want the alias's last use %v", at, end)
	}
}
