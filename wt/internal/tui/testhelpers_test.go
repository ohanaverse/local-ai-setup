package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// compactModelList builds a 78x22 list.Model from buildTable rows for models
// (no config, inventory or rotation marker; header as the list title via
// styleTableTitle) with ThemedListDelegate at ShowDescription=false and
// Spacing 0 — the production picker layout. Tests use it instead of
// hand-rolling list.New so filter/wrap/cursor assertions run against it.
func compactModelList(t *testing.T, models []config.Model) list.Model {
	t.Helper()
	tbl := buildTable(tableInput{models: models, usage: newUsageStore()}, newRefcountStore(), "")
	delegate := ThemedListDelegate(themes.Default)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	listItems := make([]list.Item, len(tbl.items))
	for i, it := range tbl.items {
		listItems[i] = it
	}
	ml := list.New(listItems, delegate, 78, 22)
	ml.Title = tbl.header
	styleTableTitle(&ml, themes.Default)
	ml.SetShowStatusBar(false)
	return ml
}

// stubUsageStore swaps the newUsageStore seam to a Store rooted at a fresh
// temp directory, so tests that build the model picker never read the
// developer's real ~/.config/agent-wt/usage.jsonl — whose event counts feed
// the picker's 1d/7d/30d columns and the 7d-usage tie-break, making displayed
// counts and row order depend on host state. Returns the stubbed store so a test can seed events via
// Record; the seam is restored on cleanup.
func stubUsageStore(t *testing.T) usage.Store {
	t.Helper()
	store := usage.NewStoreAt(t.TempDir())
	old := newUsageStore
	newUsageStore = func() usage.Store { return store }
	t.Cleanup(func() { newUsageStore = old })
	return store
}

// stubRefcountStore swaps the newRefcountStore seam to a Store rooted at a
// fresh temp directory, so tests that build the model picker through a path
// that calls newRefcountStore() directly (rather than passing their own
// Store into buildTable) never read the developer's real
// ~/.config/agent-wt/refcount.jsonl — whose live "in use" counts would
// otherwise make the ref column (and any assertion on it) depend on host
// state. Mirrors stubUsageStore's isolation of usage.jsonl.
func stubRefcountStore(t *testing.T) refcount.Store {
	t.Helper()
	store := refcount.NewStoreAt(t.TempDir())
	old := newRefcountStore
	newRefcountStore = func() refcount.Store { return store }
	t.Cleanup(func() { newRefcountStore = old })
	return store
}

// TestMain stubs the live-provider inventory so no test in this package ever
// probes the developer's real ollama/omlx/mtplx servers or reads their model
// directories, and stubs startModel so no test can start a real model
// process. Tests that need local rows call stubInventory; tests that exercise
// the start flow call stubStartModel.
func TestMain(m *testing.M) {
	runInventory = localmodels.OnDiskSnapshotForTest
	startModel = func(context.Context, *config.Config, lifecycle.Target, lifecycle.Options) error {
		return errors.New("startModel not stubbed in this test")
	}
	// The launch-time route check (#192): unstubbed, a test that launches a
	// running local model through LiteLLM would rewrite the developer's real
	// config.yaml and restart their proxy. Both forms are stubbed — the
	// non-blocking first attempt reports "done, nothing changed", which is the
	// launch-in-this-same-Update path.
	tryEnsureModelRoute = func(io.Writer, *config.Config, config.Model) (bool, bool) { return false, true }
	ensureModelRoute = func(io.Writer, *config.Config, config.Model) bool { return false }
	// Post-exit seams: no test may rewrite the real refcount file or offer to
	// stop the developer's running models.
	releaseSession = func() {}
	runStopPhase = func(*config.Config) {}
	os.Exit(m.Run())
}

// stubInventory makes both enterModelPhase and newPickModel see snap (via the
// runInventory seam) for the duration of a test.
func stubInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { runInventory = old })
}

// drainCmds executes cmd and feeds each resulting message back through Update,
// following the command chain (tea.Batch included) the way the tea runtime
// would, and returns the settled model. A test asserting on state a command
// produces — a refreshed table, a repopulated filter — must drain it: calling
// the command and discarding its message proves nothing, because the message
// is what applies the change.
func drainCmds(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for i := 0; len(queue) > 0; i++ {
		if i > 32 {
			t.Fatal("drainCmds: command chain did not settle")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		// tea.Batch returns a BatchMsg carrying the commands to run, not a
		// message Update knows how to handle; unwrap it instead of routing it.
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		next, nextCmd := m.Update(msg)
		m = next.(model)
		queue = append(queue, nextCmd)
	}
	return m
}

// selectedModelID returns the ID of the model under the picker cursor, so
// cursor assertions stay valid regardless of the table's sort order.
func selectedModelID(m model) string {
	it, ok := m.models.SelectedItem().(*modelItem)
	if !ok {
		return ""
	}
	return it.model.ID
}

// routeStub is what stubEnsureRoute hands a test: the knobs that script the
// launch-time route check and the record of what the flow did with it.
type routeStub struct {
	// changed is what the stubbed check reports: true (the default) means "the
	// route was missing and was written", so the proxy restarts.
	changed bool
	// busy makes the non-blocking first attempt (tryEnsureModelRoute) report
	// that it got nowhere — a config.yaml lock another wt holds. The flow then
	// owes a retry through the blocking form, which is what a test sees as a
	// second "ensure:<id>" event before the wait.
	busy bool
	// ensureOutput is written by the stubbed check into the writer the flow
	// handed it, the way lifecycle prints the "updated" line during the
	// synchronous write; waitOutput is written into that same writer by the
	// stubbed waitPendingRoutes, the way the check's async restart prints its
	// warnings until the wait returns.
	ensureOutput, waitOutput string
	// events is each check as "ensure:<model id>" and each wait for the proxy
	// as "wait", in order.
	events []string
	// waitEntered and waitRelease, when set, make the stubbed wait block the
	// way a real proxy restart does: it closes waitEntered once it is inside
	// and returns only when waitRelease is closed. Tests that run the routing
	// command on its own goroutine use them to hold the restart open.
	waitEntered, waitRelease chan struct{}

	// outs is the writer each check was handed, in call order: one entry per
	// "ensure:" event. The flow must hand the retry the first attempt's
	// writer, or half of the check's output ends up somewhere nobody reads.
	outs []io.Writer
}

// stubEnsureRoute swaps the three launch-time route check seams
// (tryEnsureModelRoute, ensureModelRoute, waitPendingRoutes) for recording
// fakes scripted by the returned routeStub. Nothing real is touched: no
// config.yaml and no proxy. All three seams are restored on cleanup. The fakes take no lock: a test either
// runs the routing command itself, on the test goroutine, or (with
// waitEntered/waitRelease) reads the stub only after every goroutine that
// settles the check has finished — settle is once-only, so the fakes still run
// on one goroutine at a time.
//
// Both forms of the check record the same "ensure:<id>" event: they are one
// check, once blocking and once not, and a test asserting on the flow reads
// better when it says "the check ran" either way. A retry therefore shows up as
// the event twice (s.busy), which is the only way the blocking form is reached
// from the launch flow at all.
func stubEnsureRoute(t *testing.T) *routeStub {
	t.Helper()
	s := &routeStub{changed: true}
	oldTry, oldEnsure, oldWait := tryEnsureModelRoute, ensureModelRoute, waitPendingRoutes
	// write prints to the writer the latest check was handed; a wait that no
	// check preceded (finishStart's) has none and prints nothing.
	write := func(text string) {
		if len(s.outs) > 0 && s.outs[len(s.outs)-1] != nil && text != "" {
			_, _ = io.WriteString(s.outs[len(s.outs)-1], text)
		}
	}
	tryEnsureModelRoute = func(out io.Writer, _ *config.Config, m config.Model) (bool, bool) {
		s.events = append(s.events, "ensure:"+m.ID)
		s.outs = append(s.outs, out)
		if s.busy {
			// The lock was held: nothing written, nothing known, nothing said.
			return false, false
		}
		write(s.ensureOutput)
		return s.changed, true
	}
	ensureModelRoute = func(out io.Writer, _ *config.Config, m config.Model) bool {
		s.events = append(s.events, "ensure:"+m.ID)
		s.outs = append(s.outs, out)
		write(s.ensureOutput)
		return s.changed
	}
	waitPendingRoutes = func() {
		s.events = append(s.events, "wait")
		if s.waitEntered != nil {
			close(s.waitEntered)
			<-s.waitRelease
		}
		write(s.waitOutput)
	}
	t.Cleanup(func() {
		tryEnsureModelRoute, ensureModelRoute, waitPendingRoutes = oldTry, oldEnsure, oldWait
	})
	return s
}

// stubRouteNotes clears pendingRouteNotes for one test and restores it after,
// so a test asserting on the captured route output neither sees another test's
// lines nor leaves its own behind.
func stubRouteNotes(t *testing.T) {
	t.Helper()
	old := pendingRouteNotes
	pendingRouteNotes = ""
	t.Cleanup(func() { pendingRouteNotes = old })
}

// routeDoneFrom runs the routing command out of the batch checkLaunchRoute
// returned and returns its routeDoneMsg. The batch also holds the one-second
// elapsed-time tick, which is left unrun: only the first command is the check.
func routeDoneFrom(t *testing.T, cmd tea.Cmd) routeDoneMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command: the routing phase must return the wait as a command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("command = %T (%d), want a batch of the routing command and its tick", batch, len(batch))
	}
	done, ok := batch[0]().(routeDoneMsg)
	if !ok {
		t.Fatalf("first batched command returned %T, want routeDoneMsg", done)
	}
	return done
}
