package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gitenv"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// compactModelList builds a 78x22 list.Model from buildTable rows for models
// (no config, inventory or rotation marker; header as the list title via
// styleTableTitle) with ThemedListDelegate at ShowDescription=false and
// Spacing 0 — the production picker layout. Tests use it instead of
// hand-rolling list.New so filter/wrap/cursor assertions run against it.
func compactModelList(t *testing.T, models []config.Model) list.Model {
	t.Helper()
	tbl := buildTable(tableInput{models: models, usage: newUsageStore()}, "")
	return tableList(tbl, 78, 22)
}

// tableList is the production picker's list over tbl at the given size:
// ThemedListDelegate at ShowDescription=false and Spacing 0, the header as the
// list title via styleTableTitle, and no status bar.
func tableList(tbl modelTable, width, height int) list.Model {
	delegate := ThemedListDelegate(themes.Default)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	items := make([]list.Item, len(tbl.items))
	for i, it := range tbl.items {
		items[i] = it
	}
	ml := list.New(items, delegate, width, height)
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
// RecordFor; the seam is restored on cleanup.
func stubUsageStore(t *testing.T) usage.Store {
	t.Helper()
	store := usage.NewStoreAt(t.TempDir())
	old := newUsageStore
	newUsageStore = func() usage.Store { return store }
	t.Cleanup(func() { newUsageStore = old })
	return store
}

// TestMain isolates what this package's tests reach: the config home, git,
// the tiers file and the worktree picker's PR lookups. The model picker reads
// the model server's catalog through a fake (fakeModels), so nothing here
// probes a provider or starts a model.
func TestMain(m *testing.M) {
	// No test may read or write the developer's real config directory —
	// config.IsolateConfigHomeForTest carries the full rationale. A test that
	// sets its own XDG_CONFIG_HOME still wins.
	_, rmConfigHome := config.IsolateConfigHomeForTest()
	// No test may find a git repository it did not make: the worktree tests
	// build theirs in temp directories — gitenv.IsolateForTest carries the
	// full rationale.
	gitenv.IsolateForTest()
	// No model table may read the developer's tiers.toml (or the one a
	// MODELTIERS_OUT in their shell names): every TIER cell is "-" unless a
	// test names a file of its own (tiers.IsolateForTest).
	tiers.IsolateForTest()
	// The worktree picker's PR lookups (loadRowPRsCmd, cleanup.FillPRs):
	// unstubbed, a test that runs a picker load's commands would run the
	// developer's gh against GitHub. A test that wants PRs passes its own
	// GatherInputs.PRList or stubs cleanup's seams for itself.
	cleanup.StubPRListForTest(func(string, string) ([]byte, error) {
		return nil, errors.New("gh pr list not stubbed in this test")
	})
	cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) {
		return nil, errors.New("gh pr list --base not stubbed in this test")
	})
	code := m.Run()
	rmConfigHome()
	os.Exit(code)
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

// stubRouteNotes clears pendingRouteNotes for one test and restores it after,
// so a test asserting on the captured route output neither sees another test's
// lines nor leaves its own behind.
func stubRouteNotes(t *testing.T) {
	t.Helper()
	old := pendingRouteNotes
	pendingRouteNotes = ""
	t.Cleanup(func() { pendingRouteNotes = old })
}

// rootRepo makes a repository with one commit on main and returns its root
// with symlinks resolved, the spelling claims are written with.
func rootRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", dir},
		{"-C", dir, "config", "user.email", "test@test"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "symbolic-ref", "HEAD", "refs/heads/main"},
		{"-C", dir, "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// linkedWorktree adds a linked worktree of root on a new branch and returns
// its path.
func linkedWorktree(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, ".worktrees", "feature")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "feature", path).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return path
}

// liveOther writes a claim on root for a real, running process (a sleep
// child killed on cleanup), so the liveness check sees a live session.
func liveOther(t *testing.T, root, agent string) int {
	t.Helper()
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Unix()
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	dir := filepath.Join(root, ".git", "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=main\nagent=%s\n", pid, started, root, agent)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return pid
}

// myClaim is the path of this process's claim file on root.
func myClaim(root string) string {
	return filepath.Join(root, ".git", "wt-sessions", strconv.Itoa(os.Getpid()))
}

// claimExists reports whether this process's claim is on root right now.
func claimExists(root string) bool { _, err := os.Stat(myClaim(root)); return err == nil }

// stubShellLaunch swaps the launch build for one that records the directory
// it was built for and runs nothing, so a test sees whether, and where, the
// command launch happened.
func stubShellLaunch(t *testing.T) *[]string {
	t.Helper()
	var dirs []string
	old := buildGatewayLaunch
	buildGatewayLaunch = func(_ string, _ config.Model, worktreePath string, _ bool, _ agents.Gateway, _ []config.Model, _ []string) (*exec.Cmd, agents.LaunchInfo, error) {
		dirs = append(dirs, worktreePath)
		return exec.Command("true"), agents.LaunchInfo{}, nil
	}
	t.Cleanup(func() { buildGatewayLaunch = old })
	return &dirs
}

// pickRow is the picker after Enter on a row at path, with the shell command
// pinned so a launch is one launch build (stubShellLaunch).
func pickRow(t *testing.T, path string) (model, tea.Cmd, *[]string) {
	t.Helper()
	launched := stubShellLaunch(t)
	m := model{cfg: &config.Config{}, phase: phaseList, width: 80, height: 24, initialAgent: "shell"}
	m, cmd := updateMsg(m, selectedEntryMsg{entry: worktree.Entry{Type: worktree.TypeCurrent, Branch: "main", Path: path}})
	return m, cmd, launched
}

// answerOnStdio points os.Stdin at a file holding answer and os.Stderr at a
// file whose text the returned func reads, until the test ends: the run
// commands' commit-point gate asks on the real stdio (the terminal is the
// user's by then), so a test answers it here.
func answerOnStdio(t *testing.T, answer string) func() string {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "stdin")
	if err := os.WriteFile(in, []byte(answer), 0o644); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = stdin, stderr
	t.Cleanup(func() { os.Stdin, os.Stderr = oldIn, oldErr; stdin.Close(); stderr.Close() })
	return func() string {
		data, _ := os.ReadFile(stderr.Name())
		return string(data)
	}
}

// TestConfigHomeIsNotTheDevelopersOwn pins TestMain's throwaway config home:
// everything wt keeps under its config directory — rotation.state, usage.jsonl,
// profile backups and locks — resolves into a temp directory
// for this package's tests, never ~/.config. Without it a test that launches a
// stub agent rewrites the developer's real rotation state on every run.
func TestConfigHomeIsNotTheDevelopersOwn(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for _, pair := range [][2]string{{"config.Dir()", config.Dir()}, {"config.RegistryPath()", config.RegistryPath()}} {
		if strings.HasPrefix(pair[1], filepath.Join(home, ".config")) || !strings.Contains(pair[1], "wt-test-config-") {
			t.Errorf("%s = %s, want a path under this package's throwaway config home", pair[0], pair[1])
		}
	}
}

// modelTestConfig is the session config most picker tests read: a native
// claude/opus cloud model and a local omlx/qwen3.8 model, both tagged "code",
// that agent "claude" can use. flowEnter turns its models into the model
// server's catalog.
func modelTestConfig() *config.Config {
	return &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude", "omlx"}},
		},
	}
}

// flowRunning is the set of local model ids flowEnter's catalog reports as
// running (launch rows); every other local model is a stopped start row. A
// test sets it with stubRunning.
var flowRunning map[string]bool

// stubRunning makes flowEnter's catalog report ids as running local models
// for the duration of a test.
func stubRunning(t *testing.T, ids ...string) {
	t.Helper()
	old := flowRunning
	flowRunning = map[string]bool{}
	for _, id := range ids {
		flowRunning[id] = true
	}
	t.Cleanup(func() { flowRunning = old })
}

// catalogFor is the model server's catalog for cfg's models, as the server
// would build it: each row's location from cfg, a cloud row (or a local one in
// flowRunning) running and launchable, any other local row stopped and
// startable, in cfg's order, each rated from the tiers file the test names
// (writeTiers), as the server rates its rows.
func catalogFor(t *testing.T, cfg *config.Config) modelapi.Catalog {
	t.Helper()
	ratings := tiers.Load()
	cat := modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z"}
	for i, m := range cfg.Models {
		loc, err := cfg.ResolveLocation(m)
		if err != nil {
			t.Fatalf("catalogFor %s: %v", m.ID, err)
		}
		r := modelapi.Row{
			ID: m.ID, GatewayID: m.ID, ProviderID: m.ProviderID, ModelName: m.ModelName, Family: m.Family,
			Location: string(loc), Tags: m.Tags, Registered: true, Native: m.Native, Order: i + 1,
			State: modelapi.StateRunning, Action: modelapi.ActionLaunch,
		}
		if p := m.Cost; p.InputPricePerMillion != nil || p.OutputPricePerMillion != nil || p.CachePricePerMillion != nil {
			r.Price = &modelapi.Price{Input: p.InputPricePerMillion, Cache: p.CachePricePerMillion, Output: p.OutputPricePerMillion}
		}
		if loc == config.LocationLocal && !flowRunning[m.ID] {
			r.State, r.Action = modelapi.StateStopped, modelapi.ActionStart
		}
		if e := ratings.Rating(m.ProviderID, m.ModelName); e.Rated() || e.HasAligned {
			r.Tier, r.TierCode, r.Confidence, r.TierSources = e.Tier, e.Code, e.Confidence, e.Sources
			if e.HasAligned {
				a := e.Aligned
				r.Aligned = &a
			}
		}
		cat.Rows = append(cat.Rows, r)
	}
	return cat
}

// flowEnter opens the model picker for agent on m the way the user does: the
// model server's catalog (catalogFor m.cfg, through a fakeModels) is loaded
// and the table built from it, m.slotChecks' profile rows included. It
// isolates the state directory and the usage store, and hands m.cfg's agents
// to the picker as the session config.
func flowEnter(t *testing.T, m model, agent string) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	return flowEnterKeepState(t, m, agent)
}

// flowEnterKeepState is flowEnter for a test that isolated (and seeded) the
// state directory and the usage store itself.
func flowEnterKeepState(t *testing.T, m model, agent string) model {
	t.Helper()
	m.client = &fakeModels{cat: catalogFor(t, m.cfg)}
	m.agent = agent
	if m.theme.Name == "" {
		m.theme = themes.Default
	}
	next, cmd := m.enterModelPhaseForAgent()
	return settle(t, next, cmd)
}

// pickModels is modelTestConfig's models as the standalone picker receives
// them from the model server (catalog.ToModel): each carries its location.
func pickModels() []config.Model {
	cfg := modelTestConfig()
	out := make([]config.Model, len(cfg.Models))
	for i, m := range cfg.Models {
		loc, _ := cfg.ResolveLocation(m)
		m.Location = loc
		out[i] = m
	}
	return out
}

// slotTable is the picker's table for "claude" over cfg with checks as its
// slot profiles, loaded from the model server's catalog (catalogFor), with
// two launches of profile/cloud in the usage store.
func slotTable(t *testing.T, cfg *config.Config, checks []profiles.SlotCheck) modelTable {
	t.Helper()
	tempStateDir(t)
	store := stubUsageStore(t)
	for i := 0; i < 2; i++ {
		if err := store.RecordFor("claude", "profile/cloud"); err != nil {
			t.Fatalf("seed usage: %v", err)
		}
	}
	m := flowEnterKeepState(t, model{cfg: cfg, agent: "claude", slotChecks: checks, width: 200, height: 40}, "claude")
	tbl := modelTable{header: m.models.Title}
	for _, it := range m.models.Items() {
		tbl.items = append(tbl.items, it.(*modelItem))
	}
	return tbl
}

// tableItem is the table's item for id.
func tableItem(t *testing.T, tbl modelTable, id string) *modelItem {
	t.Helper()
	for _, it := range tbl.items {
		if it.model.ID == id {
			return it
		}
	}
	t.Fatalf("the table has no row %q", id)
	return nil
}

// remotePickerFor is the model picker for agent over a model server whose
// catalog holds rows, loaded the way the user loads it (flowEnter's path), at
// 80x24 with a fresh worktree chosen. agent may be any registered driver
// name, a test's own included: the session config names it with every
// provider.
func remotePickerFor(t *testing.T, agent string, rows ...modelapi.Row) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	cfg := &config.Config{DefaultTag: "code", Agents: []config.Agent{{Name: agent, AllProviders: true}}}
	f := &fakeModels{cat: modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z", Rows: rows},
		gw: agents.Gateway{Origin: "http://127.0.0.1:1", Token: "gateway-token"}}
	m := model{cfg: cfg, client: f,
		theme: themes.Default, agent: agent, phase: phaseAgent, width: 80, height: 24, selectedPath: t.TempDir()}
	next, cmd := m.enterModelPhaseForAgent()
	next = settle(t, next, cmd)
	if next.phase != phaseModel {
		t.Fatalf("phase = %v, status %q: want the table", next.phase, next.status)
	}
	return next
}

// withServer attaches to m a model server whose catalog is catalogFor(m.cfg),
// as a session with a configured server has; nil cfg is an empty catalog.
func withServer(t *testing.T, m model) model {
	t.Helper()
	cat := modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z"}
	if m.cfg != nil {
		cat = catalogFor(t, m.cfg)
	}
	m.client = &fakeModels{cat: cat}
	if m.theme.Name == "" {
		m.theme = themes.Default
	}
	return m
}

// settleCatalog runs the catalog fetch cmd started (phaseCatalog) and feeds
// its answer back, the way the runtime does; any other state is returned
// as it is.
func settleCatalog(t *testing.T, m model, cmd tea.Cmd) (model, tea.Cmd) {
	t.Helper()
	if m.phase != phaseCatalog || cmd == nil {
		return m, cmd
	}
	return updateMsg(m, cmd())
}
