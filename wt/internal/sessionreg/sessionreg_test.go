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
	if err := r.Register("", nil); err == nil {
		t.Fatal("empty id must be an error")
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
