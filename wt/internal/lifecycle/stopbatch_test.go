package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// batchLog records, in order, every `ollama stop` an env runs and every
// /api/ps probe its daemon answers, so a test can tell sends from waits.
type batchLog struct {
	mu     sync.Mutex
	events []string
}

func (l *batchLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

func (l *batchLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

// batchEnv is cleanOllamaStop with every `ollama stop` logged, and refused
// (exit 1, "boom") for the names in refuse.
func batchEnv(log *batchLog, refuse ...string) *env {
	e := cleanOllamaStop()
	e.runEnv = func(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
		log.add("stop " + args[len(args)-1])
		if slices.Contains(refuse, args[len(args)-1]) {
			return []byte("boom\n"), errors.New("exit 1")
		}
		return nil, nil
	}
	return e
}

// loggedPs is a daemon whose /api/ps lists, on the nth probe (from 1),
// loaded(n), and logs each probe.
func loggedPs(t *testing.T, log *batchLog, loaded func(n int) []string) string {
	t.Helper()
	srv, _ := psSequence(t, func(n int) ([]string, bool) {
		log.add("probe")
		return loaded(n), true
	})
	return srv.URL
}

func ollamaEntry(name string) localmodels.Entry {
	return localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/" + name, ModelName: name, Running: true}
}

// TestStopBatchSharesOneBound pins #379's second item: three ollama models
// each still finishing a long request cost one wait, not three. Every `ollama
// stop` is sent before the first probe, the first model's wait spends the
// whole shared bound (5 sleeps here), and the two after it get one probe each
// and no sleep. Waiting serially, the batch sat out three bounds (9 minutes
// in production) before `wt stop --all` reached the omlx halt or the exit
// flow its summary. Each still-listed model is a *StillFinishingError that
// names the batch's whole wait.
func TestStopBatchSharesOneBound(t *testing.T) {
	log := &batchLog{}
	url := loggedPs(t, log, func(int) []string { return []string{"a", "b", "c"} })
	e := batchEnv(log)
	cfg := provCfg("ollama", url)
	entries := []localmodels.Entry{ollamaEntry("a"), ollamaEntry("b"), ollamaEntry("c")}

	b := beginStops(context.Background(), e, cfg, entries)
	if got := strings.Join(log.events, ","); got != "stop a,stop b,stop c" {
		t.Fatalf("after BeginStops: events = %s, want the three sends and no probe", got)
	}
	for _, en := range entries {
		owed, err := b.Stop(context.Background(), en)
		var sf *StillFinishingError
		if !errors.As(err, &sf) || owed {
			t.Fatalf("Stop(%s) = (%v, %v), want (false, *StillFinishingError)", en.ModelName, owed, err)
		}
		if sf.Waited != 5*time.Nanosecond {
			t.Errorf("Stop(%s): waited = %v, want the batch's 5ns", en.ModelName, sf.Waited)
		}
	}
	// a: 1 probe + 5 after each sleep; b and c: one probe each, nothing left.
	if got := log.count("probe"); got != 8 {
		t.Errorf("probes = %d, want 8 (one bound for the batch, not one per model: 18)", got)
	}
	if got := log.count("stop "); got != 3 {
		t.Errorf("sends = %d, want 3 (each model's stop sent once)", got)
	}
}

// TestStopBatchModelsUnloadingTogether verifies the shared wait reports each
// model as soon as it is gone: models finishing their requests while an
// earlier one is still waited for are found gone on their first probe, and a
// later one still gets what the batch has left.
func TestStopBatchModelsUnloadingTogether(t *testing.T) {
	log := &batchLog{}
	url := loggedPs(t, log, func(n int) []string {
		switch {
		case n <= 2:
			return []string{"a", "b", "c"}
		case n <= 3:
			return []string{"b", "c"}
		}
		return []string{"c"}
	})
	e := batchEnv(log)
	e.ollamaUnloadTimeout = 10 * time.Nanosecond
	b := beginStops(context.Background(), e, provCfg("ollama", url), []localmodels.Entry{ollamaEntry("a"), ollamaEntry("b"), ollamaEntry("c")})
	for _, name := range []string{"a", "b"} {
		if _, err := b.Stop(context.Background(), ollamaEntry(name)); err != nil {
			t.Fatalf("Stop(%s) = %v, want nil", name, err)
		}
	}
	var sf *StillFinishingError
	if _, err := b.Stop(context.Background(), ollamaEntry("c")); !errors.As(err, &sf) || sf.Waited != 10*time.Nanosecond {
		t.Fatalf("Stop(c) = %v, want *StillFinishingError after the batch's 10ns", err)
	}
	// a: probes 1-3 (gone on 3); b: probe 4 (gone); c: probe 5 and then the 8
	// sleeps the batch has left, one probe after each.
	if got := log.count("probe"); got != 13 {
		t.Errorf("probes = %d, want 13", got)
	}
}

// TestStopBatchRefusedStopSpendsNoWait verifies a stop ollama refused is
// reported at once with ollama's message and takes nothing from the shared
// bound, so the accepted stop after it still has the whole wait.
func TestStopBatchRefusedStopSpendsNoWait(t *testing.T) {
	log := &batchLog{}
	url := loggedPs(t, log, func(n int) []string {
		if n <= 4 {
			return []string{"a", "b"}
		}
		return []string{"a"}
	})
	e := batchEnv(log, "a")
	b := beginStops(context.Background(), e, provCfg("ollama", url), []localmodels.Entry{ollamaEntry("a"), ollamaEntry("b")})
	if _, err := b.Stop(context.Background(), ollamaEntry("a")); err == nil || err.Error() != "boom" {
		t.Fatalf("Stop(a) = %v, want ollama's own message", err)
	}
	if _, err := b.Stop(context.Background(), ollamaEntry("b")); err != nil {
		t.Fatalf("Stop(b) = %v, want nil: b left /api/ps on the 5th probe, inside the untouched bound", err)
	}
	if acc := b.Accepted(); len(acc) != 0 {
		t.Errorf("Accepted = %v, want none: every stop has reported", acc)
	}
}

// TestStopBatchAcceptedAfterCancel verifies what a Ctrl+C leaves: the models
// whose stop ollama accepted and whose Stop has not reported, the one in
// flight included, and never one ollama refused. The stop loop names them,
// since ollama unloads them whatever wt does next.
func TestStopBatchAcceptedAfterCancel(t *testing.T) {
	log := &batchLog{}
	url := loggedPs(t, log, func(int) []string { return []string{"a", "b", "c"} })
	e := batchEnv(log, "c")
	e.ollamaUnloadTimeout = 100 * time.Nanosecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := beginStops(ctx, e, provCfg("ollama", url), []localmodels.Entry{ollamaEntry("a"), ollamaEntry("b"), ollamaEntry("c")})
	e.probeClient = &http.Client{Transport: &afterTrip{after: func(n int) {
		if n == 2 {
			cancel()
		}
	}}}
	if _, err := b.Stop(ctx, ollamaEntry("a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop(a) = %v, want context.Canceled", err)
	}
	var names []string
	for _, en := range b.Accepted() {
		names = append(names, en.ModelName)
	}
	if !slices.Equal(names, []string{"a", "b"}) {
		t.Fatalf("Accepted = %v, want [a b]: c's stop was refused", names)
	}
}

// TestStopWaitNoticeAfterTwoSeconds pins #379's third item at the engine: a
// stop still waiting for ollama after stopNoticeAfter says so once, through
// the function the caller put on the context, with what is left of the wait
// as its "up to". In production that is after two 1-second sleeps, with "up
// to 3m0s" for the first model of a batch; here 2ns of 1ns sleeps, so the
// notice comes after the third probe and never again.
func TestStopWaitNoticeAfterTwoSeconds(t *testing.T) {
	log := &batchLog{}
	url := loggedPs(t, log, func(n int) []string {
		if n <= 6 {
			return []string{"a"}
		}
		return nil
	})
	e := batchEnv(log)
	e.ollamaUnloadTimeout = 10 * time.Nanosecond
	e.stopNoticeAfter = 2 * time.Nanosecond
	var notices []string
	var probesAtNotice int
	ctx := WithStopNotice(context.Background(), func(n StopNotice) {
		notices = append(notices, n.Msg)
		probesAtNotice = log.count("probe")
	})
	if err := stopModel(ctx, e, provCfg("ollama", url), "ollama", "a"); err != nil {
		t.Fatalf("stopModel = %v, want nil: a left /api/ps on the 7th probe", err)
	}
	want := []string{"waiting for ollama to finish a request in flight (up to 10ns)"}
	if !slices.Equal(notices, want) {
		t.Fatalf("notices = %q, want %q", notices, want)
	}
	if probesAtNotice != 3 {
		t.Errorf("notice after probe %d, want 3 (after 2ns of waiting)", probesAtNotice)
	}

	// A model gone within the notice's delay gives none.
	notices = nil
	quick := &batchLog{}
	url = loggedPs(t, quick, func(n int) []string {
		if n <= 2 {
			return []string{"a"}
		}
		return nil
	})
	e = batchEnv(quick)
	e.stopNoticeAfter = 2 * time.Nanosecond
	if err := stopModel(ctx, e, provCfg("ollama", url), "ollama", "a"); err != nil || len(notices) != 0 {
		t.Fatalf("quick unload: err = %v, notices = %q, want nil and none", err, notices)
	}
}

// TestStopWaitNoticeProductionWording pins the production notice: two
// seconds in, and the bound printed as a duration a person reads ("3m0s").
func TestStopWaitNoticeProductionWording(t *testing.T) {
	e := defaultEnv()
	if e.stopNoticeAfter != 2*time.Second {
		t.Fatalf("stopNoticeAfter = %v, want 2s", e.stopNoticeAfter)
	}
	if got := roundWait(newUnloadWait(e).remaining()); got != "3m0s" {
		t.Fatalf("bound = %s, want 3m0s", got)
	}
	if got := roundWait(2*time.Minute + 57*time.Second + 400*time.Millisecond); got != "2m57s" {
		t.Errorf("rounded = %s, want 2m57s", got)
	}
}

// TestStopModelDeferredStillFinishingStaysAnError pins the strict side of the
// new outcome: a caller of the engine's stop that needs the model gone before
// it goes on (anything checking err != nil — the way a start that must free
// memory or an exclusivity check would) still fails on a model ollama has not
// unloaded. The outcome is a typed error, *StillFinishingError, not a nil;
// only the stop loop, which reports it and goes on, reads it as its own
// outcome. Its message is the one a failed wait gave before.
func TestStopModelDeferredStillFinishingStaysAnError(t *testing.T) {
	log := &batchLog{}
	url := loggedPs(t, log, func(int) []string { return []string{"a"} })
	b := beginStops(context.Background(), batchEnv(log), provCfg("ollama", url), []localmodels.Entry{ollamaEntry("a")})
	owed, err := b.Stop(context.Background(), ollamaEntry("a"))
	var sf *StillFinishingError
	if err == nil || !errors.As(err, &sf) || owed {
		t.Fatalf("Stop = (%v, %v), want (false, *StillFinishingError)", owed, err)
	}
	want := "ollama stop a: /api/ps still lists the model after waiting 5ns (ollama may still be finishing a request; `ollama ps` shows whether it has unloaded)"
	if err.Error() != want {
		t.Fatalf("err = %s\nwant  %s", err, want)
	}
}
