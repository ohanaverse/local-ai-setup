package modelclient

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// defaultInterval is the heartbeat interval: the server forgets a client
// session after 60 s without a beat (modelserver's sessionTTL), so 20 s
// leaves two missed beats of slack.
const defaultInterval = 20 * time.Second

// NewSessionID returns a random UUID v4 from crypto/rand.
func NewSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails (crypto/rand panics instead)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// SessionOptions configure a Session's heartbeat.
type SessionOptions struct {
	// Interval between beats; 0 is defaultInterval (20 s).
	Interval time.Duration
	// Note receives the loop's one-line notes (failure, recovery, a refused
	// token); nil drops them.
	Note func(string)
	// Tick returns the channel the next beat waits on; nil is time.After.
	Tick func(time.Duration) <-chan time.Time
}

// Session is this client's registration with the model server: the models it
// uses, kept alive by a heartbeat goroutine until End. It is safe for
// concurrent use.
type Session struct {
	c  *Client
	id string
	o  SessionOptions

	// beforeReregister, when set (tests), runs after a 410 beat and before
	// the re-register, with the loop's context.
	beforeReregister func(context.Context)

	mu      sync.Mutex // guards the fields below and orders registrations
	models  []string
	started bool
	ended   bool
	cancel  context.CancelFunc
	done    chan struct{}

	endOnce sync.Once
	endErr  error
}

// NewSession returns an unstarted client session for models with a fresh id.
func (c *Client) NewSession(models []string, o SessionOptions) *Session {
	if o.Interval <= 0 {
		o.Interval = defaultInterval
	}
	if o.Note == nil {
		o.Note = func(string) {}
	}
	if o.Tick == nil {
		o.Tick = time.After
	}
	return &Session{c: c, id: NewSessionID(), o: o, models: slices.Clone(models)}
}

// ID is the client session's id.
func (s *Session) ID() string { return s.id }

// Start registers the client session and starts its heartbeat. The heartbeat
// runs on its own context, independent of ctx and of every later call's: only
// End stops it. A register that failed for a reason a later beat can heal
// (the server unreachable, a transient error) still starts the heartbeat,
// whose first beat re-registers once the server answers; a refused token, an
// API skew or another program at the address starts nothing. Either way the
// register error is returned for the caller to report.
func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.ended {
		return nil
	}
	err := s.c.Register(ctx, s.id, s.models)
	if permanent(err) {
		return err
	}
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.started, s.cancel, s.done = true, cancel, make(chan struct{})
	go s.loop(lctx, s.done)
	return err
}

// permanent reports whether err is one no retry can heal.
func permanent(err error) bool {
	var ua *UnauthorizedError
	var sk *SkewError
	var nm *NotModelServerError
	return errors.As(err, &ua) || errors.As(err, &sk) || errors.As(err, &nm)
}

// Add puts ids into the set and re-registers the full set (register
// replaces). After End it does nothing.
func (s *Session) Add(ctx context.Context, ids ...string) error {
	return s.change(ctx, func(ms []string) []string {
		for _, id := range ids {
			if !slices.Contains(ms, id) {
				ms = append(ms, id)
			}
		}
		return ms
	})
}

// Remove takes id out of the set and re-registers the full set. After End it
// does nothing.
func (s *Session) Remove(ctx context.Context, id string) error {
	return s.change(ctx, func(ms []string) []string {
		return slices.DeleteFunc(ms, func(m string) bool { return m == id })
	})
}

func (s *Session) change(ctx context.Context, f func([]string) []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil
	}
	s.models = f(slices.Clone(s.models))
	return s.c.Register(ctx, s.id, s.models)
}

// End stops the heartbeat, waits for its goroutine to return (so no beat in
// flight can re-register afterwards — though a re-register already on the wire
// when End cancels can still land after the DELETE, and the server drops it at
// its 60 s TTL), then deletes the client session on a
// context ctx cannot cancel, bounded at 3 s. A second call returns the first
// call's result and sends nothing. End can also wait on an in-flight
// Add/Remove/Start register, which holds the session mutex, bounded by that
// call's own context or the 10 s request timeout.
func (s *Session) End(ctx context.Context) error {
	s.endOnce.Do(func() {
		s.mu.Lock()
		cancel, done := s.cancel, s.done
		s.mu.Unlock()
		if cancel != nil {
			cancel() // first, so a beat holding mu lets go
		}
		s.mu.Lock() // waits for an Add or Remove in flight
		s.ended = true
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		s.endErr = s.c.End(ctx, s.id)
	})
	return s.endErr
}

// loop beats on every tick until ctx is cancelled or the token is refused.
func (s *Session) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.o.Tick(s.o.Interval):
		}
		if ctx.Err() != nil {
			return
		}
		err := s.beat(ctx)
		if ctx.Err() != nil {
			return
		}
		var ua *UnauthorizedError
		switch {
		case err == nil:
			if failing {
				s.o.Note("model server heartbeat recovered")
				failing = false
			}
		case errors.As(err, &ua):
			// The loop stops: no retry heals a stale token. Say what that
			// costs, too — a lapsed client session reaches auto-stop's idle
			// clock, which only beats and a start job ever touch.
			s.o.Note("model server heartbeat refused the token — the token may have been rotated; check token_file under [model-server] in config.toml (the client session is no longer renewed; restart wt to pick up the new token — a model only this session registered can be auto-stopped by the server once it sits idle past the grace)")
			return
		default:
			if !failing {
				s.o.Note("model server heartbeat failed (" + reason(err) + ") — the client session keeps trying and your agent is not affected")
				failing = true
			}
		}
	}
}

// beat renews the client session; a 410 (the server forgot it) re-registers
// the full set. The beat itself adds no models: the set is kept only by
// registers, so a beat cannot race a Remove.
func (s *Session) beat(ctx context.Context) error {
	known, err := s.c.Heartbeat(ctx, s.id, nil)
	if err != nil || known {
		return err
	}
	if s.beforeReregister != nil {
		s.beforeReregister(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.ended {
		return ctx.Err()
	}
	return s.c.Register(ctx, s.id, s.models)
}

// reason is err's short cause for a note.
func reason(err error) string {
	var ue *UnreachableError
	if errors.As(err, &ue) {
		return clean(ue.Reason)
	}
	return clean(err.Error())
}
