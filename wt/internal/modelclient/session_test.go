package modelclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// sessFake is a model server's client-session registry: register replaces,
// a heartbeat of an unknown id is 410, DELETE removes.
type sessFake struct {
	*fixture
	mu        sync.Mutex
	sessions  map[string][]string
	registers [][]string
	beats     int
	deletes   int
	// beat, when set, answers a heartbeat instead of the registry; it
	// returns false when it wrote nothing and the registry should answer.
	beat func(w http.ResponseWriter, r *http.Request, n int) bool
	// registered receives the models of every register (buffered).
	registered chan []string
	// beaten receives each heartbeat's number (buffered).
	beaten chan int
	// deleted receives one value per DELETE (buffered).
	deleted chan struct{}
}

func newSessFake(t *testing.T) *sessFake {
	t.Helper()
	sf := &sessFake{fixture: newFixture(t), sessions: map[string][]string{}, registered: make(chan []string, 64), beaten: make(chan int, 64), deleted: make(chan struct{}, 64)}
	sf.mux.HandleFunc("POST /control/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req modelapi.SessionRegister
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		sf.mu.Lock()
		sf.sessions[req.ID] = req.Models
		sf.registers = append(sf.registers, req.Models)
		sf.mu.Unlock()
		sf.registered <- req.Models
		w.WriteHeader(204)
	})
	sf.mux.HandleFunc("POST /control/v1/sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		sf.mu.Lock()
		n := sf.beats
		sf.beats++
		fn := sf.beat
		_, known := sf.sessions[r.PathValue("id")]
		sf.mu.Unlock()
		defer func() { sf.beaten <- n }()
		if fn != nil && fn(w, r, n) {
			return
		}
		if !known {
			apiErr(w, 410, modelapi.CodeUnknownSession, "gone")
			return
		}
		w.WriteHeader(204)
	})
	sf.mux.HandleFunc("DELETE /control/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		sf.mu.Lock()
		delete(sf.sessions, r.PathValue("id"))
		sf.deletes++
		sf.mu.Unlock()
		sf.deleted <- struct{}{}
		w.WriteHeader(204)
	})
	return sf
}

func (sf *sessFake) has(id string) bool {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	_, ok := sf.sessions[id]
	return ok
}

func (sf *sessFake) restart() {
	sf.mu.Lock()
	sf.sessions = map[string][]string{}
	sf.mu.Unlock()
}

func (sf *sessFake) nRegisters() int {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return len(sf.registers)
}

// notes collects the heartbeat loop's notes.
type notes struct {
	mu sync.Mutex
	ns []string
}

func (n *notes) note(s string) {
	n.mu.Lock()
	n.ns = append(n.ns, s)
	n.mu.Unlock()
}

func (n *notes) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string{}, n.ns...)
}

// tickOpts is SessionOptions driven by the test: each send on the returned
// channel is one heartbeat tick, and a send returns only once the loop took it.
func tickOpts(n *notes) (SessionOptions, chan time.Time) {
	ticks := make(chan time.Time)
	return SessionOptions{Note: n.note, Tick: func(time.Duration) <-chan time.Time { return ticks }}, ticks
}

func wait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func tick(t *testing.T, ticks chan<- time.Time) {
	t.Helper()
	select {
	case ticks <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("the heartbeat loop did not take the tick")
	}
}

// TestHeartbeatGoneReregistersTheFullSet pins that a beat the server answers
// 410 (it forgot the client session) re-registers every model in the set,
// the ones Add brought in included.
func TestHeartbeatGoneReregistersTheFullSet(t *testing.T) {
	sf := newSessFake(t)
	var n notes
	o, ticks := tickOpts(&n)
	s := sf.client().NewSession([]string{"a"}, o)
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	wait(t, sf.registered, "register")
	if err := s.Add(ctx(), "b"); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, sf.registered, "add"); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Add registered %v", got)
	}
	sf.restart()
	tick(t, ticks)
	if got := wait(t, sf.registered, "re-register"); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("re-registered %v, want [a b]", got)
	}
	if err := s.Remove(ctx(), "a"); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, sf.registered, "remove"); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("Remove registered %v", got)
	}
	_ = s.End(ctx())
	if ns := n.all(); len(ns) != 0 {
		t.Errorf("notes = %q, want none (a 410 re-registers silently)", ns)
	}
}

// TestHeartbeatGoneBeforeTheFirstBeatReregisters pins that a server restart
// between Start and the first tick is healed by the first beat, silently.
func TestHeartbeatGoneBeforeTheFirstBeatReregisters(t *testing.T) {
	sf := newSessFake(t)
	var n notes
	o, ticks := tickOpts(&n)
	s := sf.client().NewSession([]string{"a"}, o)
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	wait(t, sf.registered, "register")
	sf.restart()
	tick(t, ticks)
	wait(t, sf.registered, "re-register")
	_ = s.End(ctx())
	if ns := n.all(); len(ns) != 0 || sf.nRegisters() != 2 {
		t.Errorf("notes %q registers %d", ns, sf.nRegisters())
	}
}

// TestHeartbeatFailureNotesOnceAndRecovers pins the transient-failure notes:
// one failure note however many beats fail, then one recovered note.
func TestHeartbeatFailureNotesOnceAndRecovers(t *testing.T) {
	sf := newSessFake(t)
	sf.beat = func(w http.ResponseWriter, _ *http.Request, n int) bool {
		if n < 3 {
			apiErr(w, 503, "x", "busy")
			return true
		}
		return false
	}
	var n notes
	o, ticks := tickOpts(&n)
	s := sf.client().NewSession([]string{"a"}, o)
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		tick(t, ticks)
		wait(t, sf.beaten, "beat")
	}
	tick(t, ticks) // the loop took it, so the fourth beat's note is out
	_ = s.End(ctx())
	want := []string{
		"model server heartbeat failed (busy) — the client session keeps trying and your agent is not affected",
		"model server heartbeat recovered",
	}
	if ns := n.all(); !slices.Equal(ns, want) {
		t.Errorf("notes = %q, want %q", ns, want)
	}
}

// TestHeartbeatTimesOutOnAHungServer pins that a server that never answers a
// beat cannot stall the loop: each beat ends at the request timeout, one note
// is written, and the loop keeps beating.
func TestHeartbeatTimesOutOnAHungServer(t *testing.T) {
	sf := newSessFake(t)
	release := make(chan struct{})
	defer close(release)
	sf.beat = func(_ http.ResponseWriter, r *http.Request, _ int) bool {
		select {
		case <-r.Context().Done():
		case <-release:
		}
		return true
	}
	var n notes
	o, ticks := tickOpts(&n)
	s := sf.client(WithRequestTimeout(30*time.Millisecond)).NewSession([]string{"a"}, o)
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		tick(t, ticks)
	}
	ns := n.all()
	if len(ns) != 1 || !strings.HasPrefix(ns[0], "model server heartbeat failed (") || !strings.Contains(ns[0], "timed out") {
		t.Errorf("notes = %q, want one timeout note", ns)
	}
	_ = s.End(ctx())
}

// TestHeartbeatUnauthorizedNotesTokenAndKeepsRunning pins that a 401 beat
// (the token was rotated under the client) writes one note naming the token
// setting and stops the loop; nothing exits, End still works.
func TestHeartbeatUnauthorizedNotesTokenAndKeepsRunning(t *testing.T) {
	sf := newSessFake(t)
	sf.beat = func(w http.ResponseWriter, _ *http.Request, _ int) bool {
		apiErr(w, 401, modelapi.CodeUnauthorized, modelapi.MsgUnauthorized)
		return true
	}
	var n notes
	o, ticks := tickOpts(&n)
	s := sf.client().NewSession([]string{"a"}, o)
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	tick(t, ticks)
	wait(t, s.done, "the loop to stop")
	select {
	case ticks <- time.Time{}:
		t.Fatal("the loop still takes ticks after a 401")
	default:
	}
	ns := n.all()
	if len(ns) != 1 || !strings.Contains(ns[0], "token_file under [model-server]") || !strings.Contains(ns[0], "rotated") || !strings.Contains(ns[0], "client session") {
		t.Errorf("notes = %q", ns)
	}
	if err := s.End(ctx()); err != nil {
		t.Errorf("End: %v", err)
	}
}

// TestEndStopsBeatsAndDeletes pins that End stops the loop and deletes the
// client session, and that a second End sends nothing more.
func TestEndStopsBeatsAndDeletes(t *testing.T) {
	sf := newSessFake(t)
	var n notes
	o, ticks := tickOpts(&n)
	s := sf.client().NewSession([]string{"a"}, o)
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	tick(t, ticks)
	wait(t, sf.beaten, "beat")
	if err := s.End(ctx()); err != nil {
		t.Fatal(err)
	}
	select {
	case ticks <- time.Time{}:
		t.Fatal("the loop still takes ticks after End")
	default:
	}
	if err := s.End(ctx()); err != nil {
		t.Fatal(err)
	}
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.deletes != 1 || len(sf.sessions) != 0 {
		t.Errorf("deletes %d sessions %v", sf.deletes, sf.sessions)
	}
}

// TestEndDuringA410BeatDoesNotReregister pins the End race: a beat that got
// 410 and is about to re-register when End runs must not leave a client
// session behind. The seam holds the beat exactly between the 410 and the
// re-register until End has cancelled the loop; after End returns the server
// holds no session for the id and saw only the first register.
func TestEndDuringA410BeatDoesNotReregister(t *testing.T) {
	sf := newSessFake(t)
	var n notes
	o, ticks := tickOpts(&n)
	s := sf.client().NewSession([]string{"a"}, o)
	at410 := make(chan struct{})
	var early atomic.Bool
	s.beforeReregister = func(c context.Context) {
		close(at410)
		<-c.Done() // End has cancelled the loop
		// End must not DELETE while this beat is still running: a DELETE
		// arriving now is an End that did not wait for the goroutine.
		select {
		case <-sf.deleted:
			early.Store(true)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	wait(t, sf.registered, "register")
	sf.restart()
	tick(t, ticks)
	wait(t, at410, "the beat to reach the re-register")
	ended := make(chan error, 1)
	go func() { ended <- s.End(ctx()) }()
	if err := wait(t, ended, "End"); err != nil {
		t.Fatal(err)
	}
	// Whatever the beat was going to do it has done once its goroutine is
	// gone (End already waited for it; this catches an End that did not).
	wait(t, s.done, "the heartbeat goroutine to return")
	if early.Load() {
		t.Error("End sent its DELETE while the heartbeat goroutine was still in its beat")
	}
	if sf.has(s.ID()) || sf.nRegisters() != 1 {
		t.Errorf("after End: session held %v, registers %d (want none, 1)", sf.has(s.ID()), sf.nRegisters())
	}
}

// TestEndIsSafeTwiceAndConcurrentWithAdd pins that End may run beside Add and
// Remove and twice (the exit path and a deferred call both reach it) without
// a race or a client session left behind.
func TestEndIsSafeTwiceAndConcurrentWithAdd(t *testing.T) {
	sf := newSessFake(t)
	var n notes
	o, _ := tickOpts(&n)
	s := sf.client().NewSession([]string{"a"}, o)
	if err := s.Start(ctx()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = s.Add(ctx(), "b") }()
		go func() { defer wg.Done(); _ = s.Remove(ctx(), "a") }()
		go func() { defer wg.Done(); _ = s.End(ctx()) }()
	}
	wg.Wait()
	if sf.has(s.ID()) {
		t.Error("a client session was left behind")
	}
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.deletes != 1 {
		t.Errorf("deletes %d, want 1", sf.deletes)
	}
}

// TestSessionIDIsUUIDv4 pins the id shape the server and logs expect, and
// that two ids differ.
func TestSessionIDIsUUIDv4(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := NewSessionID(), NewSessionID()
	if !re.MatchString(a) || !re.MatchString(b) || a == b {
		t.Errorf("ids %q %q", a, b)
	}
	s := New(Endpoint{URL: "http://127.0.0.1:1", Token: tok}).NewSession(nil, SessionOptions{})
	if !re.MatchString(s.ID()) {
		t.Errorf("session id %q", s.ID())
	}
}

// TestDefaultHeartbeatIntervalIs20s pins the beat interval against the
// server's 60 s client-session TTL (modelserver's sessionTTL): 20 s leaves
// two missed beats before a client session lapses.
func TestDefaultHeartbeatIntervalIs20s(t *testing.T) {
	if defaultInterval != 20*time.Second {
		t.Errorf("defaultInterval = %v", defaultInterval)
	}
}
