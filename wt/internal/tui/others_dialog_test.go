// Tests for the picker's question about other providers' models (#412): the
// dialog a start opens when the engine reports models of other provider
// families loaded beside the target, its answers, and --others for the pinned
// row.
package tui

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// Residents the dialog tests list: an mtplx model whose memory use is
// unknown, an ollama model with a figure, and an mlx_lm_server pairing wt
// cannot stop.
var (
	mtplxOther  = lifecycle.Resident{Entry: localmodels.Entry{ProviderID: "mtplx", ModelID: "mtplx/qwen3.8-35b"}, Stoppable: true}
	ollamaOther = lifecycle.Resident{Entry: localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/gemma4:12b", LoadedSize: 9_600_000_000}, Stoppable: true}
	mlxOther    = lifecycle.Resident{Entry: localmodels.Entry{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/pair"}}
)

// othersRefusal is the engine's refusal for a start beside others, with the
// same-family occupants named by occupants and the families it cannot read.
func othersRefusal(occupants []string, others []lifecycle.Resident, unknown ...string) *lifecycle.OccupiedError {
	occ := occupiedBy(occupants)
	occ.Others, occ.OthersUnknown = others, unknown
	return occ
}

// othersDialog drives a start of omlx/qwen3.8 whose first attempt the engine
// refuses with refusal and every later one starts, and returns the dialog.
func othersDialog(t *testing.T, refusal *lifecycle.OccupiedError) (model, *startCalls) {
	t.Helper()
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	calls := stubStartModel(t, func(call int, _ context.Context, _ lifecycle.Target, _ lifecycle.Options) error {
		if call == 1 {
			return refusal
		}
		return nil
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if got.phase != phaseReplaceConfirm || got.replace == nil {
		t.Fatalf("phase = %v, want the dialog", got.phase)
	}
	return got, calls
}

// choiceTitles lists the dialog's choices by title.
func choiceTitles(m model) []string {
	var titles []string
	for _, it := range m.replace.choices.Items() {
		titles = append(titles, it.(choiceItem).title)
	}
	return titles
}

// chooseTitled moves the dialog's cursor to the choice titled title and
// presses Enter.
func chooseTitled(t *testing.T, m model, title string) model {
	t.Helper()
	for i, it := range m.replace.choices.Items() {
		if it.(choiceItem).title == title {
			m.replace.choices.Select(i)
			next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			if next.start != nil && next.start.cancel != nil {
				t.Cleanup(next.start.cancel)
			}
			return next
		}
	}
	t.Fatalf("no choice %q in %v", title, choiceTitles(m))
	return m
}

// TestOthersDialogListsOtherProvidersModels pins the dialog's header (#412):
// under "Also loaded, by other providers:" one line per resident, its id and
// the memory it holds as wt knows it ("memory use unknown" when the server
// does not say, never 0), with "(in use by N wt session(s))" when wt
// sessions use it, then a "cannot tell" line per family wt could not read.
// The same-family occupants keep their "will stop" lines above. Without it
// the user picks "keep" or "stop" about models they cannot see, or a session
// in another terminal loses its model unannounced.
func TestOthersDialogListsOtherProvidersModels(t *testing.T) {
	origin := localmodels.FamilyOrigin(startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), "mlx_lm_server")
	cases := []struct {
		name      string
		occupants []string
		want      string
	}{
		{"others only", nil,
			"Also loaded, by other providers:\n" +
				"  mtplx/qwen3.8-35b — memory use unknown (in use by 1 wt session(s))\n" +
				"  ollama/gemma4:12b — 9.6 GB loaded\n" +
				"  cannot tell whether mlx_lm_server at " + origin + " is serving a model"},
		{"same family and others", []string{"omlx/old"},
			"Starting omlx/qwen3.8 will stop:\n" +
				"  omlx/old (in use by 2 wt session(s))\n" +
				"Also loaded, by other providers:\n" +
				"  mtplx/qwen3.8-35b — memory use unknown (in use by 1 wt session(s))\n" +
				"  ollama/gemma4:12b — 9.6 GB loaded\n" +
				"  cannot tell whether mlx_lm_server at " + origin + " is serving a model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubSessionCounts(t, map[string]int{"mtplx/qwen3.8-35b": 1, "omlx/old": 2})
			got, _ := othersDialog(t, othersRefusal(c.occupants, []lifecycle.Resident{mtplxOther, ollamaOther}, "mlx_lm_server"))
			if got.replace.header != c.want {
				t.Errorf("header = %q, want %q", got.replace.header, c.want)
			}
			// Every line is on the 80x24 screen in full, a long one wrapped
			// onto the next: compared word by word.
			view := got.View()
			words := strings.Join(strings.Fields(view), " ")
			for _, line := range strings.Split(c.want, "\n") {
				if !strings.Contains(words, strings.Join(strings.Fields(line), " ")) {
					t.Errorf("screen lacks %q; screen:\n%s", strings.TrimSpace(line), view)
				}
			}
		})
	}
}

// TestOthersDialogChoices pins the dialog's choices (#412): Cancel first and
// under the cursor, then keep, then stop, whose description counts the models
// it stops; with same-family occupants as well the two actions say they
// replace. Keep's description says what it does in each dialog: beside the
// models listed, or, with same-family occupants, after replacing the ones
// listed first, or, with no model listed at all (a family wt cannot read),
// without stopping anything. Stop is absent when nothing listed can be
// stopped (an mlx_lm_server pairing, a family wt cannot read). A default on
// anything but Cancel stops or loads models on a stray Enter; a stop choice
// over unstoppable models promises what wt cannot do; a keep that says it
// loads beside models it is about to replace hides the replace.
func TestOthersDialogChoices(t *testing.T) {
	cases := []struct {
		name    string
		refusal *lifecycle.OccupiedError
		titles  []string
		descs   []string
	}{
		{"others", othersRefusal(nil, []lifecycle.Resident{mtplxOther, ollamaOther, mlxOther}),
			[]string{"Cancel", "Start and keep them", "Stop them, then start"},
			[]string{"Return to the model screen", "Load omlx/qwen3.8 beside the models above", "Stop 2 model(s) of other providers first"}},
		{"others and same family", othersRefusal([]string{"omlx/old"}, []lifecycle.Resident{mtplxOther}),
			[]string{"Cancel", "Replace, keep the others", "Replace and stop the others"},
			[]string{"Return to the model screen", "Replace the models listed first, keep the other providers' models", "Stop 1 model(s) of other providers first"}},
		{"nothing stoppable", othersRefusal(nil, []lifecycle.Resident{mlxOther}, "mtplx"),
			[]string{"Cancel", "Start and keep them"},
			[]string{"Return to the model screen", "Load omlx/qwen3.8 beside the models above"}},
		{"only a family wt cannot read", othersRefusal(nil, nil, "mtplx"),
			[]string{"Cancel", "Start and keep them"},
			[]string{"Return to the model screen", "Load omlx/qwen3.8 without stopping anything"}},
		{"same family only keeps its dialog", othersRefusal([]string{"omlx/old"}, nil),
			[]string{"Cancel", "Replace and start"},
			[]string{"Return to the model screen", "Stop the running model, then start this one"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubSessionCounts(t, nil)
			got, _ := othersDialog(t, c.refusal)
			if titles := choiceTitles(got); !reflect.DeepEqual(titles, c.titles) {
				t.Errorf("choices = %v, want %v", titles, c.titles)
			}
			var descs []string
			for _, it := range got.replace.choices.Items() {
				descs = append(descs, it.(choiceItem).desc)
			}
			if !reflect.DeepEqual(descs, c.descs) {
				t.Errorf("descriptions = %v, want %v", descs, c.descs)
			}
			if got.replace.choices.Index() != 0 {
				t.Errorf("cursor on choice %d, want 0 (Cancel)", got.replace.choices.Index())
			}
		})
	}
}

// TestOthersDialogAnswersReachTheEngine pins what each answer starts with
// (#412): keep retries once with the question Decided and nothing to stop;
// stop retries once with exactly the ids the dialog showed that wt can stop,
// never an unstoppable one; AllowReplace only when same-family occupants were
// shown and so confirmed. A wrong Stop list stops a model the user never saw,
// or fails the start on one wt cannot stop; AllowReplace without the same-
// family lines replaces a model nobody was asked about.
func TestOthersDialogAnswersReachTheEngine(t *testing.T) {
	cases := []struct {
		name      string
		occupants []string
		choice    string
		allow     bool
		stop      []string
	}{
		{"keep", nil, "Start and keep them", false, nil},
		{"stop", nil, "Stop them, then start", false, []string{"mtplx/qwen3.8-35b", "ollama/gemma4:12b"}},
		{"replace and keep", []string{"omlx/old"}, "Replace, keep the others", true, nil},
		{"replace and stop", []string{"omlx/old"}, "Replace and stop the others", true, []string{"mtplx/qwen3.8-35b", "ollama/gemma4:12b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubSessionCounts(t, nil)
			got, calls := othersDialog(t, othersRefusal(c.occupants, []lifecycle.Resident{mtplxOther, mlxOther, ollamaOther}, "mlx_lm_server"))
			next := chooseTitled(t, got, c.choice)
			if next.phase != phaseStarting {
				t.Fatalf("phase = %v, want the retry in flight", next.phase)
			}
			waitStartCalls(t, calls, 2)
			o := calls.at(1).opts
			if !o.Others.Decided || !reflect.DeepEqual(o.Others.Stop, c.stop) {
				t.Errorf("Others = %+v, want Decided with Stop %v", o.Others, c.stop)
			}
			if o.AllowReplace != c.allow {
				t.Errorf("AllowReplace = %v, want %v", o.AllowReplace, c.allow)
			}
			if calls.at(1).target != omlxTarget {
				t.Errorf("retry target = %+v, want the same model", calls.at(1).target)
			}
		})
	}
}

// TestOthersDialogBackingOutStopsNothing pins the ways out of the dialog
// (#412): Enter on the default (Cancel) and esc return to the model screen,
// ctrl+c quits wt, and none of them calls the engine again. A retry on any of
// them would stop or load models the user declined.
func TestOthersDialogBackingOutStopsNothing(t *testing.T) {
	keys := map[string]tea.KeyMsg{
		"enter on Cancel": {Type: tea.KeyEnter},
		"esc":             {Type: tea.KeyEsc},
		"ctrl+c":          {Type: tea.KeyCtrlC},
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			stubSessionCounts(t, nil)
			got, calls := othersDialog(t, othersRefusal(nil, []lifecycle.Resident{mtplxOther}))
			next, cmd := updateMsg(got, key)
			if name == "ctrl+c" {
				if cmd == nil {
					t.Fatal("ctrl+c returned no command, want tea.Quit")
				}
				if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Error("ctrl+c did not quit")
				}
			} else if next.phase != phaseModel {
				t.Errorf("phase = %v, want the model screen", next.phase)
			}
			if next.start != nil {
				t.Error("a start is in flight after backing out")
			}
			if calls.len() != 1 {
				t.Errorf("startModel calls = %d, want 1", calls.len())
			}
		})
	}
}

// TestFirstStartAsksAboutOthers pins the picker's first attempt (#412): the
// question about other providers' models is left unanswered, so the engine
// refuses with what is loaded and the dialog can ask. A first attempt that
// answered it would load the model beside, or stop, models the user never
// saw. The retry after the plain same-family dialog still asks.
func TestFirstStartAsksAboutOthers(t *testing.T) {
	got, calls := othersDialog(t, othersRefusal([]string{"omlx/old"}, nil))
	chooseTitled(t, got, "Replace and start")
	waitStartCalls(t, calls, 2)
	for i := 0; i < 2; i++ {
		if o := calls.at(i).opts.Others; o.Decided || len(o.Stop) != 0 {
			t.Errorf("call %d: Others = %+v, want the question unanswered", i+1, o)
		}
	}
	if !calls.at(1).opts.AllowReplace {
		t.Error("the confirmed replace did not set AllowReplace")
	}
}

// TestOthersFlagAnswersForThePinnedRowOnly pins --others in the picker
// (#412), by --replace's rule: it answers the question for the -M pinned row
// without the dialog — keep on the first attempt, stop with every resident
// wt can stop once the engine has said what is loaded, as the non-TUI start
// does — and never for another row, which still gets the dialog. A flag that
// leaked to rows the user browsed to would stop models nobody asked about; one
// the pin ignored would stop `wt -M <id> --others=stop` at a dialog.
func TestOthersFlagAnswersForThePinnedRowOnly(t *testing.T) {
	refusal := othersRefusal(nil, []lifecycle.Resident{mtplxOther, mlxOther, ollamaOther}, "mlx_lm_server")
	cases := []struct {
		name, mode, pin string
		wantDialog      bool
		want            []lifecycle.OthersAnswer
	}{
		{"stop, pinned", "stop", "omlx/qwen3.8", false,
			[]lifecycle.OthersAnswer{{}, {Decided: true, Stop: []string{"mtplx/qwen3.8-35b", "ollama/gemma4:12b"}}}},
		{"keep, pinned", "keep", "omlx/qwen3.8", false,
			[]lifecycle.OthersAnswer{{Decided: true}}},
		{"stop, another row pinned", "stop", "omlx/other", true, []lifecycle.OthersAnswer{{}}},
		{"keep, another row pinned", "keep", "omlx/other", true, []lifecycle.OthersAnswer{{}}},
		{"unset, pinned", "", "omlx/qwen3.8", true, []lifecycle.OthersAnswer{{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubSessionCounts(t, nil)
			m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
			m.othersMode, m.pinnedModel = c.mode, c.pin
			calls := stubStartModel(t, func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
				if opts.Others.Decided {
					return nil
				}
				return refusal
			})
			got, _ := enterStartRow(t, m, "omlx/qwen3.8")
			got, _ = updateMsg(got, recvStart(t, got))
			if got.start != nil && got.start.cancel != nil {
				t.Cleanup(got.start.cancel)
			}
			if dialog := got.phase == phaseReplaceConfirm; dialog != c.wantDialog {
				t.Fatalf("phase = %v, want the dialog: %v", got.phase, c.wantDialog)
			}
			waitStartCalls(t, calls, len(c.want))
			if calls.len() != len(c.want) {
				t.Fatalf("startModel calls = %d, want %d", calls.len(), len(c.want))
			}
			for i, w := range c.want {
				if o := calls.at(i).opts.Others; !reflect.DeepEqual(o, w) {
					t.Errorf("call %d: Others = %+v, want %+v", i+1, o, w)
				}
			}
		})
	}
}

// TestOthersStopFlagStillAsksAboutTheSameFamily pins --others=stop on a pin
// whose start would also replace a model of its own provider, without
// --replace (#412): the flag answers only the others, so the plain replace
// dialog asks about the same-family model, and confirming it retries once
// with AllowReplace and the stop the flag gave. Folding the replace into the
// flag would stop a model --replace was never given for; dropping the flag's
// answer at the dialog would ask again or stop nothing.
func TestOthersStopFlagStillAsksAboutTheSameFamily(t *testing.T) {
	stubSessionCounts(t, nil)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.othersMode, m.pinnedModel = "stop", "omlx/qwen3.8"
	calls := stubStartModel(t, func(call int, _ context.Context, _ lifecycle.Target, _ lifecycle.Options) error {
		if call == 1 {
			return othersRefusal([]string{"omlx/old"}, []lifecycle.Resident{mtplxOther, mlxOther})
		}
		return nil
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if got.phase != phaseReplaceConfirm {
		t.Fatalf("phase = %v, want the replace dialog", got.phase)
	}
	if titles := choiceTitles(got); !reflect.DeepEqual(titles, []string{"Cancel", "Replace and start"}) {
		t.Errorf("choices = %v, want the plain replace dialog", titles)
	}
	if want := "Starting omlx/qwen3.8 will stop:\n  omlx/old"; got.replace.header != want {
		t.Errorf("header = %q, want %q", got.replace.header, want)
	}
	chooseTitled(t, got, "Replace and start")
	waitStartCalls(t, calls, 2)
	o := calls.at(1).opts
	if !o.AllowReplace || !reflect.DeepEqual(o.Others, lifecycle.OthersAnswer{Decided: true, Stop: []string{"mtplx/qwen3.8-35b"}}) {
		t.Errorf("retry = AllowReplace %v Others %+v, want AllowReplace and the flag's stop", o.AllowReplace, o.Others)
	}
}

// TestPinnedStartTakesTheOthersFlag pins the hop from the -M pin's own start
// (enterModelPhase, which begins the start without Enter) to the engine:
// --others=keep reaches its first attempt. Without it the flag would answer
// only a retry of the pin from the picker, not the launch it was given for.
func TestPinnedStartTakesTheOthersFlag(t *testing.T) {
	tempStateDir(t)
	calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: "omlx/qwen3.8", othersMode: "keep", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	got, _ := m.enterModelPhase("claude", models, "code")
	if got.start != nil && got.start.cancel != nil {
		t.Cleanup(got.start.cancel)
	}
	waitStartCalls(t, calls, 1)
	if o := calls.at(0).opts.Others; !o.Decided || len(o.Stop) != 0 {
		t.Errorf("Others = %+v, want Decided with nothing to stop", o)
	}
}

// TestRunArgsReachModelOthers pins the Run(...) → model hop for --others:
// the value lands on the model, where the pinned row's start reads it.
// Without it the flag would be parsed and silently dropped by every picker
// launch.
func TestRunArgsReachModelOthers(t *testing.T) {
	for _, mode := range []string{"", "keep", "stop"} {
		m := newRunModel(false, false, mode, "", "omlx/qwen3.8", "", "", nil, themes.Theme{}, "", &config.Config{})
		if m.othersMode != mode {
			t.Errorf("--others=%q: model.othersMode = %q", mode, m.othersMode)
		}
	}
}

// TestProfileOthersDialogKeepsTheOwedRestart pins the others dialog of a
// slot profile's local-model start (#412). The refusal touched nothing, so
// the restart the profile's cloud-route write owes is kept: keep and stop
// carry it on the one retry, and Cancel, esc and quitting wt each settle it
// exactly once. Spending it at the refusal restarts the proxy twice; dropping
// it leaves the written cloud route unpublished, and that slot answers
// "Invalid model name".
func TestProfileOthersDialogKeepsTheOwedRestart(t *testing.T) {
	dialog := func(t *testing.T) (model, *slotStub, *startCalls) {
		t.Helper()
		stubSessionCounts(t, nil)
		calls := stubStartModel(t, func(call int, _ context.Context, _ lifecycle.Target, _ lifecycle.Options) error {
			if call == 1 {
				return othersRefusal(nil, []lifecycle.Resident{mtplxOther})
			}
			return nil
		})
		starting, stub := mixedStarting(t, true)
		got, _ := updateMsg(starting, recvStart(t, starting))
		if got.phase != phaseReplaceConfirm || got.slot == nil || !got.slot.owed {
			t.Fatalf("phase = %v slot = %+v, want the dialog with the flow kept", got.phase, got.slot)
		}
		return got, stub, calls
	}
	for _, choice := range []string{"Start and keep them", "Stop them, then start"} {
		t.Run(choice+" carries it", func(t *testing.T) {
			got, stub, calls := dialog(t)
			next := chooseTitled(t, got, choice)
			waitStartCalls(t, calls, 2)
			if c := calls.at(1); !c.opts.RestartOwed || !c.opts.Others.Decided || c.target != omlxTarget {
				t.Errorf("retry = %+v, want RestartOwed and a decided answer for the same model", c)
			}
			if next.phase != phaseStarting || next.slot == nil {
				t.Errorf("phase = %v slot = %v, want the retry in flight with the flow kept", next.phase, next.slot)
			}
			if strings.Contains(stub.got(), "settle") {
				t.Error("the owed restart was settled although the retry carries it")
			}
		})
	}
	leave := map[string]tea.KeyMsg{"cancel": {Type: tea.KeyEnter}, "esc": {Type: tea.KeyEsc}}
	for name, key := range leave {
		t.Run(name+" settles it once", func(t *testing.T) {
			got, stub, calls := dialog(t)
			next, _ := updateMsg(got, key)
			if next.phase != phaseModel || next.slot != nil {
				t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", next.phase, next.slot)
			}
			if got := stub.got(); got != "ensure:openrouter/a:owed=false,wait,settle" {
				t.Errorf("events = %q, want exactly one settle", got)
			}
			if calls.len() != 1 {
				t.Errorf("startModel calls = %d, want 1", calls.len())
			}
			assertNothingRecorded(t)
		})
	}
	t.Run("quitting wt settles it once", func(t *testing.T) {
		got, stub, _ := dialog(t)
		quit, cmd := updateMsg(got, tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Fatal("ctrl+c returned no command, want tea.Quit")
		}
		var out bytes.Buffer
		flushRouteNotesAfterRun(quit, &out)
		if got := stub.got(); got != "ensure:openrouter/a:owed=false,wait,settle" {
			t.Errorf("events = %q, want exactly one settle", got)
		}
	})
}

// TestProfileOthersFlagIsThePinnedProfilesAnswer pins --others for a slot
// profile's local model (#412): it answers when the profile is the -M pin and
// never when the profile was chosen in the picker under another pin, by
// --replace's rule. A leak would load a model beside, or stop, models of a
// launch nobody gave the flag for.
func TestProfileOthersFlagIsThePinnedProfilesAnswer(t *testing.T) {
	cases := []struct {
		name, pin string
		want      lifecycle.OthersAnswer
	}{
		{"another row pinned", "profile/cloud", lifecycle.OthersAnswer{}},
		{"this row pinned", "profile/mixed", lifecycle.OthersAnswer{Decided: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
			m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile(), mixedProfile())
			stubSlotSeams(t)
			m.othersMode, m.pinnedModel = "keep", c.pin
			preparing, cmd := enterProfileRow(t, m, "profile/mixed")
			starting, _ := updateMsg(preparing, slotStageFrom(t, cmd)())
			if starting.start != nil && starting.start.cancel != nil {
				t.Cleanup(starting.start.cancel)
			}
			waitStartCalls(t, calls, 1)
			if got := calls.at(0); !reflect.DeepEqual(got.opts.Others, c.want) || got.target != omlxTarget {
				t.Errorf("start = %+v, want Others %+v for the profile's local model", got, c.want)
			}
		})
	}
}
