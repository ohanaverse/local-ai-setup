package tui

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// fakeModels is a ModelClient that answers from memory: no network, no port.
// catErrs are returned one per Catalog call, then the catalog is answered.
type fakeModels struct {
	mu       sync.Mutex
	cat      modelapi.Catalog
	catErrs  []error
	catCalls int

	resolveRow modelapi.Row
	resolveErr error
	resolved   []string

	// startFn scripts RunStart: n is the 1-based call number. nil fails the
	// start.
	startFn func(ctx context.Context, n int, req modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error)
	starts  []modelapi.StartRequest
	// cancelled are the job ids CancelJob was asked for.
	cancelled []string
	// onCancel, when set, runs after CancelJob records its id.
	onCancel func(string)
	// cancelErr is what CancelJob answers (the server refusing the cancel).
	cancelErr error
	// answerers are the Answerer each RunStart call was handed.
	answerers []modelclient.Answerer
	// registerBlock, when set, holds the client session's first register
	// until it is closed or the register's context is cancelled.
	registerBlock chan struct{}
	// registerErr is what the client session's first register returns.
	registerErr error
	// log is every start, cancel and client session call, in order:
	// "register a,b", "add x", "remove x", "end", "start x", "cancel j".
	log []string
}

func (f *fakeModels) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, s)
}

// calls in order, a copy.
func (f *fakeModels) history() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

func (f *fakeModels) startReqs() []modelapi.StartRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

func (f *fakeModels) cancels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cancelled)
}

// fakeSession is a ClientSession that writes its calls to its fake's log.
type fakeSession struct {
	f      *fakeModels
	models []string
}

func (s *fakeSession) Start(ctx context.Context) error {
	s.f.mu.Lock()
	block := s.f.registerBlock
	s.f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			s.f.record("register cancelled")
			return ctx.Err()
		}
	}
	s.f.record("register " + strings.Join(s.models, ","))
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return s.f.registerErr
}
func (s *fakeSession) Add(_ context.Context, ids ...string) error {
	s.f.record("add " + strings.Join(ids, ","))
	return nil
}
func (s *fakeSession) Remove(_ context.Context, id string) error {
	s.f.record("remove " + id)
	return nil
}
func (s *fakeSession) End(context.Context) error {
	s.f.record("end")
	return nil
}

func (f *fakeModels) Catalog(context.Context) (modelapi.Catalog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catCalls++
	if len(f.catErrs) > 0 {
		err := f.catErrs[0]
		f.catErrs = f.catErrs[1:]
		return modelapi.Catalog{}, err
	}
	return f.cat, nil
}

func (f *fakeModels) Resolve(_ context.Context, id string) (modelapi.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, id)
	return f.resolveRow, f.resolveErr
}

func (f *fakeModels) RunStart(ctx context.Context, req modelapi.StartRequest, ans modelclient.Answerer, prog modelclient.Progress) (modelapi.JobStatus, error) {
	f.record("start " + req.Model)
	f.mu.Lock()
	f.starts = append(f.starts, req)
	f.answerers = append(f.answerers, ans)
	n, fn := len(f.starts), f.startFn
	f.mu.Unlock()
	if fn == nil {
		return modelapi.JobStatus{}, errors.New("RunStart is not scripted in this test")
	}
	return fn(ctx, n, req, prog)
}
func (f *fakeModels) CancelJob(_ context.Context, id string) error {
	f.record("cancel " + id)
	f.mu.Lock()
	f.cancelled = append(f.cancelled, id)
	hook, err := f.onCancel, f.cancelErr
	f.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return err
}
func (f *fakeModels) NewSession(models []string, _ modelclient.SessionOptions) ClientSession {
	return &fakeSession{f: f, models: slices.Clone(models)}
}
func (f *fakeModels) Gateway() agents.Gateway { return agents.Gateway{} }
func (f *fakeModels) Now() time.Time          { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) }

func (f *fakeModels) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.catCalls
}

// wireRow is a catalog row a test edits.
func wireRow(id, family string, mut func(*modelapi.Row)) modelapi.Row {
	prov, name, _ := strings.Cut(id, "/")
	r := modelapi.Row{
		ID: id, GatewayID: id, ProviderID: prov, ModelName: name, Family: family,
		Location: "cloud", Tags: []string{"code"}, Registered: true,
		State: modelapi.StateRunning, Action: modelapi.ActionLaunch,
	}
	if mut != nil {
		mut(&r)
	}
	return r
}

// remoteConfig is the session config the picker reads: one agent that takes
// every provider, and the default tag the header names.
func remoteConfig() *config.Config {
	return &config.Config{DefaultTag: "code", Agents: []config.Agent{{Name: "claude", AllProviders: true}}}
}

// remoteModel is the picker on the agent screen with the server fake attached,
// ready for the user's Enter on the claude row (enterModelPhaseForAgent).
func remoteModel(t *testing.T, f *fakeModels, width, height int) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	return model{
		cfg: remoteConfig(), client: f, theme: themes.Default, agent: "claude", phase: phaseAgent,
		width: width, height: height,
	}
}

// settle runs cmd and feeds its message back through Update, once.
func settle(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command to run")
	}
	next, _ := updateMsg(m, cmd())
	return next
}

func plain(m model) string { return ansi.Strip(m.View()) }

func smallCatalog() modelapi.Catalog {
	return modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z", Rows: []modelapi.Row{
		wireRow("openrouter/glm-5.3", "glm", func(r *modelapi.Row) { r.Order = 1; r.Price = &modelapi.Price{Input: f64(0.5), Output: f64(2)} }),
		wireRow("openrouter/kimi-3", "kimi", func(r *modelapi.Row) { r.Order = 2 }),
	}}
}

// TestModelPhaseFetchesInACommand pins T12: entering the model phase on the
// API path only returns a command and shows a loading screen. The catalog is
// asked for inside that command, never in Update, and ctrl+c leaves the
// loading screen.
func TestModelPhaseFetchesInACommand(t *testing.T) {
	f := &fakeModels{cat: smallCatalog()}
	m := remoteModel(t, f, 80, 24)
	m, cmd := m.enterModelPhaseForAgent()
	if f.calls() != 0 {
		t.Fatalf("Catalog was called %d times inside Update, want 0", f.calls())
	}
	if cmd == nil || m.phase != phaseCatalog {
		t.Fatalf("phase = %v, cmd nil = %v: want the loading phase and a fetch command", m.phase, cmd == nil)
	}
	if !strings.Contains(plain(m), "loading") {
		t.Errorf("loading view = %q, want it to say loading", plain(m))
	}
	if _, quit := updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC}); quit == nil || quit() != tea.Quit() {
		t.Errorf("ctrl+c while loading did not quit")
	}
	m = settle(t, m, cmd)
	if f.calls() != 1 || m.phase != phaseModel {
		t.Fatalf("after the fetch: %d catalog calls, phase %v, want 1 and the table", f.calls(), m.phase)
	}
	if got := len(m.models.Items()); got != 2 {
		t.Errorf("table has %d rows, want 2", got)
	}
}

// TestLoadingAndFetchErrorViewsFit40x12 holds L13 for the two screens this
// task adds: the loading view and the failed-fetch view, with an error as long
// as a real server error, fit the smallest terminal.
func TestLoadingAndFetchErrorViewsFit40x12(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {80, 24}, {40, 5}} {
		loading, failed := catalogScreens(t, size[0], size[1])
		assertFits(t, "loading", loading, size[0], size[1])
		assertFits(t, "fetch failed", failed, size[0], size[1])
	}
	_, failed := catalogScreens(t, 40, 12)
	if !strings.Contains(ansi.Strip(failed), "F retry") {
		t.Errorf("failed view = %q, want the F retry hint", ansi.Strip(failed))
	}
}

// catalogScreens is the loading view and the failed-fetch view at a size.
func catalogScreens(t *testing.T, width, height int) (loading, failed string) {
	t.Helper()
	long := errors.New("model server at http://127.0.0.1:8099 is not reachable (connection refused) — start it with 'wt-model serve --listen 127.0.0.1:8099 --token-file PATH', or fix url under [model-server] in config.toml")
	f := &fakeModels{cat: smallCatalog(), catErrs: []error{long}}
	m := remoteModel(t, f, width, height)
	m, cmd := m.enterModelPhaseForAgent()
	m = resized(t, m, width, height)
	loading = m.View()
	m = settle(t, m, cmd)
	return loading, m.View()
}

// TestCatalogFetchFailureShowsStatusAndRetries pins T10 and T5: a failed fetch
// is an `error: ` status with the server's text, the keys that act are named
// only while it shows, and F fetches again and shows the table.
func TestCatalogFetchFailureShowsStatusAndRetries(t *testing.T) {
	f := &fakeModels{cat: smallCatalog(), catErrs: []error{errors.New("model server at http://x:1 is not reachable (refused) — start it with 'wt-model serve'")}}
	m := remoteModel(t, f, 80, 24)
	m, cmd := m.enterModelPhaseForAgent()
	if strings.Contains(plain(m), "F retry") {
		t.Errorf("loading footer = %q, must not offer F retry yet", plain(m))
	}
	m = settle(t, m, cmd)
	if m.phase != phaseCatalog || !strings.HasPrefix(m.status, "error: ") {
		t.Fatalf("phase %v, status %q: want the failed fetch to stay on its screen with an error: status", m.phase, m.status)
	}
	for _, want := range []string{"not reachable", "wt-model serve", "F retry"} {
		if !strings.Contains(plain(m), want) {
			t.Errorf("failed view = %q, want %q", plain(m), want)
		}
	}
	// Keys that do nothing here do not clear the failure.
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !strings.HasPrefix(m.status, "error: ") {
		t.Fatalf("a stray key cleared the failure: %q", m.status)
	}
	m, cmd = updateMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	if cmd == nil || strings.Contains(plain(m), "F retry") {
		t.Fatalf("F: cmd nil = %v, view %q: want a fetch and no retry hint while it runs", cmd == nil, plain(m))
	}
	if f.calls() != 1 {
		t.Fatalf("Catalog was called inside Update: %d calls", f.calls())
	}
	m = settle(t, m, cmd)
	if f.calls() != 2 || m.phase != phaseModel || m.status != "" {
		t.Fatalf("after retry: %d calls, phase %v, status %q, want the table", f.calls(), m.phase, m.status)
	}
	if strings.Contains(plain(m), "F retry") {
		t.Errorf("the table footer offers F retry with nothing failed: %q", plain(m))
	}
}

// TestTableRowsFromWire pins what a wire row draws: the price the server
// stated and its `~` marker with the cost legend, the tier cell from tier_code
// or the bare score, the native band first, and the in-use digit from the
// server's client session count.
func TestTableRowsFromWire(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	rows := []modelapi.Row{
		wireRow("openrouter/zeta", "zeta", func(r *modelapi.Row) {
			r.TierCode, r.Tier, r.Aligned = "S", "standard", f64(44)
			r.Price = &modelapi.Price{Input: f64(0.5), Cache: f64(0.1), Output: f64(2), TimePriced: true}
			r.Sessions = 3
		}),
		wireRow("openrouter/alpha", "alpha", func(r *modelapi.Row) { r.Aligned = f64(61) }),
		wireRow("claude/sonnet", "claude", func(r *modelapi.Row) { r.Native = true; r.Location = "native" }),
		wireRow("openrouter/none", "none", nil),
	}
	tr := wireTableRows(rows, at)
	sortRows(tr)
	refs := map[string]int{}
	for _, r := range tr {
		refs[r.Model.ID] = r.Wire.Sessions
	}
	tbl := renderTable(tr, nil, "claude", refs, "")
	var ids []string
	for _, it := range tbl.items {
		ids = append(ids, it.model.ID)
	}
	if ids[0] != "claude/sonnet" {
		t.Errorf("order = %v, want the native model first", ids)
	}
	line := func(id string) string {
		for _, it := range tbl.items {
			if it.model.ID == id {
				return ansi.Strip(it.Title())
			}
		}
		t.Fatalf("no row %s", id)
		return ""
	}
	zeta := line("openrouter/zeta")
	for _, want := range []string{"S44", " 0.5000", " 2.0000~", "3 "} {
		if !strings.Contains(zeta, want) {
			t.Errorf("zeta row = %q, want %q", zeta, want)
		}
	}
	if !strings.HasPrefix(zeta, "3 ") {
		t.Errorf("zeta row = %q, want the in-use digit 3 first", zeta)
	}
	if !strings.Contains(tbl.header, "COST (~ varies by time)") {
		t.Errorf("header = %q, want the cost legend for a time-priced row", tbl.header)
	}
	alpha := line("openrouter/alpha")
	if !strings.Contains(alpha, "61") || strings.Contains(alpha, "S61") {
		t.Errorf("alpha row = %q, want the bare score 61 for a row with no tier code", alpha)
	}
	if none := line("openrouter/none"); strings.Contains(none, "~") {
		t.Errorf("none row = %q, want no price mark", none)
	}
	if strings.Index(zeta, "~") < 0 || strings.Contains(alpha, "~") {
		t.Errorf("only the time-priced row carries ~: zeta %q alpha %q", zeta, alpha)
	}
}

// TestPinnedModelUsesResolve pins a -M pin through the server's resolve: the
// model id is what is asked, the table opens on that row, and an unknown model
// comes back to the agent picker with the server's own message.
func TestPinnedModelUsesResolve(t *testing.T) {
	f := &fakeModels{cat: smallCatalog()}
	f.resolveRow = f.cat.Rows[1]
	m := remoteModel(t, f, 80, 24)
	m.pinnedModel = "openrouter/kimi-3"
	m, cmd := m.enterModelPhaseForAgent()
	if len(f.resolved) != 0 {
		t.Fatalf("Resolve ran inside Update: %v", f.resolved)
	}
	m = settle(t, m, cmd)
	if len(f.resolved) != 1 || f.resolved[0] != "openrouter/kimi-3" {
		t.Fatalf("resolved = %v, want the pinned id", f.resolved)
	}
	// The pinned launch row launches (Task 13): the launching screen, with
	// the table's cursor on the pinned row behind it.
	if m.phase != phaseLaunching || selectedModelID(m) != "openrouter/kimi-3" {
		t.Errorf("phase %v, cursor %q, want the pinned row launching", m.phase, selectedModelID(m))
	}

	const msg = `unknown model "openrouter/nope": the registry has no such id`
	g := &fakeModels{cat: smallCatalog(), resolveErr: &modelclient.UnknownModelError{Message: msg}}
	n := remoteModel(t, g, 80, 24)
	n.pinnedModel = "openrouter/nope"
	n, cmd = n.enterModelPhaseForAgent()
	n = settle(t, n, cmd)
	if n.phase != phaseAgent || n.status != msg || n.pinnedModel != "" {
		t.Errorf("phase %v, status %q, pin %q: want the agent picker, the server's message and the pin dropped", n.phase, n.status, n.pinnedModel)
	}
}

// TestServerTextCannotForgeATableCell pins M11 at the cell: a family and a
// block reason carrying ESC and a newline (a fake client returns them raw,
// which the real client already cleans) draw as one printable line in the
// table and in the line under it.
func TestServerTextCannotForgeATableCell(t *testing.T) {
	cat := smallCatalog()
	cat.Rows = append(cat.Rows, wireRow("openrouter/evil", "fam\x1b[2Jily\nFORGED", func(r *modelapi.Row) {
		r.Order = 3
		r.Action = modelapi.ActionBlock
		r.BlockReason = "bad\x1b[2J\x1b]0;pwn\x07reason\nFORGED row"
	}))
	f := &fakeModels{cat: cat}
	m := remoteModel(t, f, 100, 24)
	m, cmd := m.enterModelPhaseForAgent()
	m = settle(t, m, cmd)
	for _, it := range m.models.Items() {
		title := it.(*modelItem).Title()
		if strings.ContainsAny(title, "\n\x1b\x07") {
			t.Errorf("a table row carries a control character: %q", title)
		}
	}
	m.models.Select(indexOfID(m, "openrouter/evil"))
	view := m.View()
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]0") {
		t.Errorf("the view carries the server's escape sequence: %q", view)
	}
	var found bool
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(line, "reason") {
			found = true
			if !strings.Contains(line, "FORGED row") {
				t.Errorf("the block reason is split across lines: %q", line)
			}
		}
	}
	if !found {
		t.Errorf("the highlighted row's block reason is not in the view: %q", ansi.Strip(view))
	}
}

// TestWireRowsDefendTheirOwnCells pins that wireTableRows itself flattens the
// text it draws: the client already cleans decoded text, so this is the second
// wall, held by a row built by hand.
func TestWireRowsDefendTheirOwnCells(t *testing.T) {
	r := wireRow("openrouter/x", "a\x1b[1mb\nc", func(r *modelapi.Row) { r.BlockReason = "x\ny\x1b[2J" })
	tr := wireTableRows([]modelapi.Row{r}, time.Time{})
	if got := tr[0].Model.Family; strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("family = %q, want one printable line", got)
	}
	if got := tr[0].BlockReason(); strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("block reason = %q, want one printable line", got)
	}
}

// TestRemoteDetailNamesClientSessions pins N7 and the detail line: a row that
// client sessions use says `N client session(s)`, never a bare "session".
func TestRemoteDetailNamesClientSessions(t *testing.T) {
	cat := smallCatalog()
	cat.Rows[0].Sessions = 2
	f := &fakeModels{cat: cat}
	m := remoteModel(t, f, 80, 24)
	m, cmd := m.enterModelPhaseForAgent()
	m = settle(t, m, cmd)
	m.models.Select(indexOfID(m, "openrouter/glm-5.3"))
	if v := plain(m); !strings.Contains(v, "2 client session(s)") {
		t.Errorf("view = %q, want the detail to say 2 client session(s)", v)
	}
	if v := plain(m); strings.Contains(v, "LiteLLM") {
		t.Errorf("view = %q, the API path names no LiteLLM mode", v)
	}
}

// TestRemotePathSkipsLegacyPhases pins that nothing on the API path reads the
// local inventory or enters the ollama or routing phases.
func TestRemotePathSkipsLegacyPhases(t *testing.T) {
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot {
		t.Error("the local inventory was probed on the API path")
		return localmodels.Snapshot{}
	}
	t.Cleanup(func() { runInventory = old })
	f := &fakeModels{cat: smallCatalog()}
	m := remoteModel(t, f, 80, 24)
	m, cmd := m.enterModelPhaseForAgent()
	m = settle(t, m, cmd)
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase == phaseOllamaWarn || m.phase == phaseRouting || m.phase == phaseStarting {
		t.Errorf("phase = %v, want none of the legacy phases", m.phase)
	}
}

// TestPinnedModelStartsOrLaunchesOnTheServer pins a -M pin on the API path as
// the registry path treats it: the pin is the user's act, so a pinned row the
// server must start runs the start flow (with --replace, the pin's
// permission), and a pinned row that can launch launches at once, never just
// selected.
func TestPinnedModelStartsOrLaunchesOnTheServer(t *testing.T) {
	cat := startRowCatalog()
	f := &fakeModels{cat: cat, resolveRow: cat.Rows[0]}
	f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, _ modelclient.Progress) (modelapi.JobStatus, error) {
		<-ctx.Done()
		return detached("j1")
	}
	m := remoteModel(t, f, 80, 24)
	m.selectedPath = t.TempDir()
	m.pinnedModel, m.allowReplace = "omlx/qwen3.8", true
	m, cmd := m.enterModelPhaseForAgent()
	m, _ = updateMsg(m, cmd())
	if m.phase != phaseStarting || m.start == nil {
		t.Fatalf("phase = %v: want the pinned start row's start", m.phase)
	}
	t.Cleanup(m.start.cancel)
	if reqs := waitStarts(t, f, 1); !reqs[0].AllowReplace || reqs[0].Model != "omlx/qwen3.8" {
		t.Errorf("start request = %+v, want the pinned model with --replace's allow_replace", reqs[0])
	}

	g := &fakeModels{cat: cat, resolveRow: cat.Rows[1]}
	n := remoteModel(t, g, 80, 24)
	n.selectedPath = t.TempDir()
	n.pinnedModel = "openrouter/glm-5.3"
	n, cmd = n.enterModelPhaseForAgent()
	n, cmd = updateMsg(n, cmd())
	if n.phase != phaseLaunching || cmd == nil || n.launchModel.ID != "openrouter/glm-5.3" {
		t.Errorf("phase %v, cmd nil %v, launch %q: want the pinned launch row launching", n.phase, cmd == nil, n.launchModel.ID)
	}
}

// waitStarts waits until the fake has seen n start requests (the start runs
// in its own goroutine) and returns them.
func waitStarts(t *testing.T, f *fakeModels, n int) []modelapi.StartRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if reqs := f.startReqs(); len(reqs) >= n {
			return reqs
		}
		if time.Now().After(deadline) {
			t.Fatalf("start requests = %d, want %d", len(f.startReqs()), n)
		}
		runtime.Gosched()
	}
}

// TestPickerRotationCursorSkipsStartRows pins that the picker's cursor and
// the non-TUI endpoint launch rotate over the same list: the launchable
// registered rows in the server's order. A start row is never the
// rotation's next, so the cursor after the last launchable row wraps to the
// first launchable one, not to a model that would have to be started.
func TestPickerRotationCursorSkipsStartRows(t *testing.T) {
	cat := modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z", Rows: []modelapi.Row{
		wireRow("omlx/qwen3.8", "qwen", func(r *modelapi.Row) {
			r.Location, r.State, r.Action, r.Order = "local", modelapi.StateStopped, modelapi.ActionStart, 1
		}),
		wireRow("openrouter/a", "a", func(r *modelapi.Row) { r.Order = 2 }),
		wireRow("openrouter/b", "b", func(r *modelapi.Row) { r.Order = 3 }),
	}}
	f := &fakeModels{cat: cat}
	m := remoteModel(t, f, 80, 24)
	if err := rotation.New().RecordFor("claude", "openrouter/b"); err != nil {
		t.Fatal(err)
	}
	m, cmd := m.enterModelPhaseForAgent()
	m = settle(t, m, cmd)
	if got := selectedModelID(m); got != "openrouter/a" {
		t.Errorf("cursor = %q, want openrouter/a (the start row is not in the rotation)", got)
	}
}
