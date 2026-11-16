package sessionreg

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newReg(ttl time.Duration) (*Registry, *clock) {
	c := &clock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	return New(ttl, c.now), c
}

// TestSessionCountsOncePerModel checks that a session listing a model twice
// counts once and two sessions on one model count twice, because the count
// drives auto-stop and must be sessions, not mentions.
func TestSessionCountsOncePerModel(t *testing.T) {
	r, _ := newReg(time.Minute)
	if err := r.Register("s1", []string{"a", "a", ""}); err != nil {
		t.Fatal(err)
	}
	if got := r.Counts([]string{"a"})["a"]; got != 1 {
		t.Fatalf("one session listing a twice: count = %d, want 1", got)
	}
	if err := r.Register("s2", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got := r.Counts([]string{"a", "b", "c"})
	if got["a"] != 2 || got["b"] != 1 || got["c"] != 0 {
		t.Fatalf("counts = %v, want a:2 b:1 c:0", got)
	}
	err := r.Register("", nil)
	if err == nil {
		t.Fatal("empty id must be an error")
	}
	if want := "the client session id is empty"; err.Error() != want {
		t.Fatalf("empty id error = %q, want %q (no package label, N7 qualifier)", err, want)
	}
}

// TestExpiredSessionStopsCounting checks that a session whose heartbeat lapsed
// stops counting with no explicit End, because a crashed client never says
// goodbye.
func TestExpiredSessionStopsCounting(t *testing.T) {
	r, c := newReg(time.Minute)
	_ = r.Register("s1", []string{"a"})
	c.t = c.t.Add(time.Minute) // exactly ttl: still live
	if r.Counts([]string{"a"})["a"] != 1 {
		t.Fatal("session at exactly ttl must still count")
	}
	c.t = c.t.Add(time.Second)
	if got := r.Counts([]string{"a"})["a"]; got != 0 {
		t.Fatalf("expired count = %d, want 0", got)
	}
	if len(r.Live()) != 0 || len(r.Users("a")) != 0 {
		t.Fatal("expired session must not appear in Live or Users")
	}
}

// TestHeartbeatAfterExpiryIsUnknown checks that a heartbeat after the TTL
// reports unknown and does not resurrect the session, because the caller must
// re-register so the model set is restated.
func TestHeartbeatAfterExpiryIsUnknown(t *testing.T) {
	r, c := newReg(time.Minute)
	_ = r.Register("s1", []string{"a"})
	if !r.Heartbeat("s1", []string{"b"}) {
		t.Fatal("live heartbeat must be known")
	}
	if got := r.Counts([]string{"b"})["b"]; got != 1 {
		t.Fatalf("heartbeat must add models, b count = %d", got)
	}
	c.t = c.t.Add(2 * time.Minute)
	if r.Heartbeat("s1", []string{"a"}) {
		t.Fatal("heartbeat after expiry must be unknown")
	}
	if got := r.Counts([]string{"a"})["a"]; got != 0 {
		t.Fatalf("expired session resurrected, count = %d", got)
	}
	if r.Heartbeat("nobody", nil) {
		t.Fatal("unknown id must be unknown")
	}
}

// TestEndedSessionStartsIdleClock checks that End records the idle start for
// the session's models, because auto-stop measures idleness from the last use.
func TestEndedSessionStartsIdleClock(t *testing.T) {
	r, c := newReg(time.Minute)
	_ = r.Register("s1", []string{"m"})
	c.t = c.t.Add(10 * time.Second)
	t0 := c.t
	r.End("s1")
	since, seen := r.IdleSince("m")
	if !seen || !since.Equal(t0) {
		t.Fatalf("IdleSince = (%v,%v), want (%v,true)", since, seen, t0)
	}
	if r.Counts([]string{"m"})["m"] != 0 {
		t.Fatal("ended session must not count")
	}
	if got := r.Seen(); len(got) != 1 || got[0] != "m" {
		t.Fatalf("Seen = %v, want [m]", got)
	}
}

// TestEndOfLapsedSessionKeepsTheIdleClock checks that an End of a session its
// TTL already expired does not stamp the idle clock with the end time, because
// a client deleting a session record long after it stopped heartbeating must
// not postpone the auto-stop of models that have been idle the whole time.
func TestEndOfLapsedSessionKeepsTheIdleClock(t *testing.T) {
	r, c := newReg(time.Minute)
	_ = r.Register("s1", []string{"m"})
	c.t = c.t.Add(10 * time.Second)
	_ = r.Heartbeat("s1", nil) // the last beat
	t0 := c.t
	c.t = c.t.Add(2 * time.Hour) // the session lapsed long before this End
	r.End("s1")
	if since, seen := r.IdleSince("m"); !seen || !since.Equal(t0) {
		t.Fatalf("IdleSince = (%v,%v), want the last beat (%v,true)", since, seen, t0)
	}
}

// TestLapsedSessionsArePrunedOnRead checks that a read path deletes a session
// whose TTL lapsed: a server that runs for days otherwise keeps every session
// whose client crashed without an End. Pruning must not change any answer,
// since the reads already skip lapsed sessions.
func TestLapsedSessionsArePrunedOnRead(t *testing.T) {
	r, c := newReg(time.Minute)
	for _, id := range []string{"s1", "s2", "s3"} {
		_ = r.Register(id, []string{"m"})
	}
	c.t = c.t.Add(time.Second) // all three still live
	_ = r.Counts([]string{"m"})
	if len(r.sessions) != 3 {
		t.Fatalf("%d sessions after a live read, want 3", len(r.sessions))
	}
	c.t = c.t.Add(time.Minute)
	_ = r.Counts([]string{"m"})
	if len(r.sessions) != 0 {
		t.Fatalf("%d sessions after the TTL passed, want all pruned", len(r.sessions))
	}
	if got := r.Counts([]string{"m"})["m"]; got != 0 {
		t.Fatalf("count after pruned = %d, want 0", got)
	}
}

// TestUnseenModelIsNotSeen checks that a never-registered model reports
// not-seen, because auto-stop must not stop models no session ever used.
func TestUnseenModelIsNotSeen(t *testing.T) {
	r, _ := newReg(time.Minute)
	since, seen := r.IdleSince("x")
	if seen || !since.IsZero() {
		t.Fatalf("IdleSince = (%v,%v), want (zero,false)", since, seen)
	}
}

// TestLiveIsIdAgnostic checks that Live reports a model id the registry was
// never told about, because callers pass ids from outside any catalog.
func TestLiveIsIdAgnostic(t *testing.T) {
	r, _ := newReg(time.Minute)
	_ = r.Register("s1", []string{"weird/model:id"})
	_ = r.Register("s2", []string{"weird/model:id"})
	if got := r.Live()["weird/model:id"]; got != 2 {
		t.Fatalf("Live = %v, want count 2", r.Live())
	}
	users := r.Users("weird/model:id")
	if len(users) != 2 || users[0] != "s1" || users[1] != "s2" {
		t.Fatalf("Users = %v, want sorted [s1 s2]", users)
	}
}

// TestTouchRefreshesOnlySeenModels checks that Touch moves a registered
// model's idle clock to now but leaves a never-registered model unseen, so a
// model started by hand can never become auto-stoppable through a start job.
func TestTouchRefreshesOnlySeenModels(t *testing.T) {
	r, c := newReg(time.Minute)
	t0 := c.t
	_ = r.Register("s", []string{"m"})
	r.End("s")
	c.t = t0.Add(time.Hour)
	r.Touch("m")
	r.Touch("unseen")
	if since, seen := r.IdleSince("m"); !seen || !since.Equal(c.t) {
		t.Fatalf("IdleSince(m) = (%v,%v), want (%v,true)", since, seen, c.t)
	}
	if _, seen := r.IdleSince("unseen"); seen {
		t.Fatal("Touch made an unseen model seen")
	}
}

// TestRegistryForgetKeepsLiveUsers checks that Forget drops a model no live
// session uses from Seen, so a model later started by hand is out of
// auto-stop's reach again (Touch does not bring it back), while a model a
// live session lists is kept: forgetting it would hide that session's model
// from auto-stop until the next heartbeat. A lapsed session keeps nothing.
func TestRegistryForgetKeepsLiveUsers(t *testing.T) {
	r, c := newReg(time.Minute)
	_ = r.Register("s1", []string{"a"})
	_ = r.Register("s2", []string{"b"})
	r.End("s2")
	r.Forget("a")
	if _, seen := r.IdleSince("a"); !seen {
		t.Fatal("Forget dropped a model a live session uses")
	}
	if got := r.Users("a"); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("Users(a) = %v, want [s1]", got)
	}
	r.Forget("b")
	if got := r.Seen(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("Seen after Forget(b) = %v, want [a]", got)
	}
	r.Touch("b")
	if _, seen := r.IdleSince("b"); seen {
		t.Fatal("Touch brought a forgotten model back")
	}
	r.Forget("never-seen")         // a no-op
	c.t = c.t.Add(2 * time.Minute) // s1 lapses
	r.Forget("a")
	if got := r.Seen(); len(got) != 0 {
		t.Fatalf("Seen after the session lapsed and Forget(a) = %v, want none", got)
	}
}
