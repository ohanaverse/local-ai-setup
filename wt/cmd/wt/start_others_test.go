package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
)

// othersFixture is the engine's refusal for a start of omlx/qwen3.8 beside
// three other providers' models: an mtplx server (no figure), an ollama
// model (9.6 GB loaded) and an mlx_lm_server pairing wt cannot stop.
func othersFixture() *lifecycle.OccupiedError {
	return &lifecycle.OccupiedError{Others: []lifecycle.Resident{
		{Entry: localmodels.Entry{ProviderID: "mtplx", ModelID: "mtplx/qwen3.8-35b", ModelName: "qwen3.8-35b"}, Stoppable: true},
		{Entry: localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/gemma4:12b", ModelName: "gemma4:12b", LoadedSize: 9_600_000_000}, Stoppable: true},
		{Entry: localmodels.Entry{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", ModelName: "p"}},
	}}
}

// othersQuestionWant is the question othersFixture asks, with mtplx's one
// wt session.
const othersQuestionWant = "starting omlx/qwen3.8 loads it beside models of other providers:\n" +
	"  mtplx/qwen3.8-35b — memory use unknown (in use by 1 wt session(s))\n" +
	"  ollama/gemma4:12b — 9.6 GB loaded\n" +
	"  mlx_lm_server/p — memory use unknown; wt cannot stop it\n" +
	"stop them first, keep them and start, or cancel? [s/k/N]"

// stubOthersEngine replaces the engine: a call that has not decided about
// the other providers' models is refused with occ (Occupants answered by
// AllowReplace are dropped from it, as the engine drops them); any other call
// starts. It records every call's Options.
func stubOthersEngine(t *testing.T, occ *lifecycle.OccupiedError) *[]lifecycle.Options {
	t.Helper()
	var mu sync.Mutex
	calls := &[]lifecycle.Options{}
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		mu.Lock()
		*calls = append(*calls, opts)
		mu.Unlock()
		refusal := &lifecycle.OccupiedError{}
		if !opts.AllowReplace {
			refusal.Occupants = occ.Occupants
		}
		if !opts.Others.Decided {
			refusal.Others, refusal.OthersUnknown = occ.Others, occ.OthersUnknown
		}
		if len(refusal.Occupants) == 0 && len(refusal.Others) == 0 && len(refusal.OthersUnknown) == 0 {
			return nil
		}
		return refusal
	}
	t.Cleanup(func() { lifecycleStart = old })
	return calls
}

// othersAsk is one question the others prompt was asked.
type othersAsk struct {
	question string
	canStop  bool
}

// stubChooseOthers answers the others prompt with choice (or err) and records
// what it was asked.
func stubChooseOthers(t *testing.T, choice othersChoice, err error) *[]othersAsk {
	t.Helper()
	asked := &[]othersAsk{}
	old := chooseOthers
	chooseOthers = func(q string, canStop bool) (othersChoice, error) {
		*asked = append(*asked, othersAsk{q, canStop})
		return choice, err
	}
	t.Cleanup(func() { chooseOthers = old })
	return asked
}

// stubOthersMode sets the --others mode for one test.
func stubOthersMode(t *testing.T, mode string) {
	t.Helper()
	old := othersMode
	othersMode = othersFlag(mode)
	t.Cleanup(func() { othersMode = old })
}

// stubSessionCounts gives every id the counts in m.
func stubSessionCounts(t *testing.T, m map[string]int) {
	t.Helper()
	old := sessionCounts
	sessionCounts = func([]string) map[string]int { return m }
	t.Cleanup(func() { sessionCounts = old })
}

// lockedBuffer is a bytes.Buffer safe to share with startProgress's ticker.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureStderr points osStderr at a buffer for one test.
func captureStderr(t *testing.T) *lockedBuffer {
	t.Helper()
	b := &lockedBuffer{}
	old := osStderr
	osStderr = b
	t.Cleanup(func() { osStderr = old })
	return b
}

// TestAskChoiceParsesTheOthersAnswer pins the parse of the others prompt:
// s/stop stops (only when stopping is offered), k/keep keeps, and anything
// else, an empty line and EOF included, cancels. A default other than cancel
// would let an accidental Enter stop, or load beside, models the user meant
// to keep to themselves.
func TestAskChoiceParsesTheOthersAnswer(t *testing.T) {
	cases := []struct {
		input   string
		canStop bool
		want    othersChoice
	}{
		{"s\n", true, othersStop},
		{"STOP\n", true, othersStop},
		{"k\n", true, othersKeep},
		{" keep \n", true, othersKeep},
		{"k\n", false, othersKeep},
		{"s\n", false, othersCancel},
		{"\n", true, othersCancel},
		{"n\n", true, othersCancel},
		{"y\n", true, othersCancel},
		{"", true, othersCancel},
	}
	for _, c := range cases {
		got, err := askChoice(strings.NewReader(c.input), c.canStop)
		if err != nil {
			t.Errorf("askChoice(%q, %v) err = %v", c.input, c.canStop, err)
		}
		if got != c.want {
			t.Errorf("askChoice(%q, %v) = %v, want %v", c.input, c.canStop, got, c.want)
		}
	}
}

// TestDriveStartAsksAboutOtherProvidersModels pins the non-TUI others prompt
// (#412): its text (each resident with its figure and its wt sessions), and
// that each answer sets the one retry's Options — stop names exactly the
// stoppable residents shown, keep stops nothing, and cancel retries nothing
// and says what is still loaded. A wrong Stop list stops a model nobody
// approved; a retry after cancel loads a model the user turned down.
func TestDriveStartAsksAboutOtherProvidersModels(t *testing.T) {
	cases := []struct {
		name    string
		choice  othersChoice
		calls   int
		stop    []string
		wantErr string
	}{
		{"stop", othersStop, 2, []string{"mtplx/qwen3.8-35b", "ollama/gemma4:12b"}, ""},
		{"keep", othersKeep, 2, nil, ""},
		{"cancel", othersCancel, 1, nil, "cancelled — mtplx/qwen3.8-35b, ollama/gemma4:12b, mlx_lm_server/p still loaded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubSignals(t)
			stubStdinTTY(t, true)
			stubOthersMode(t, "")
			stubSessionCounts(t, map[string]int{"mtplx/qwen3.8-35b": 1})
			asked := stubChooseOthers(t, c.choice, nil)
			calls := stubOthersEngine(t, othersFixture())

			err := startForLaunch(&config.Config{}, startTestRow(), false)
			if c.wantErr == "" && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if c.wantErr != "" && (err == nil || err.Error() != c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if len(*asked) != 1 || (*asked)[0].question != othersQuestionWant || !(*asked)[0].canStop {
				t.Errorf("asked = %+v, want one question, stop offered:\n%s", *asked, othersQuestionWant)
			}
			if len(*calls) != c.calls {
				t.Fatalf("engine calls = %d, want %d", len(*calls), c.calls)
			}
			if (*calls)[0].Others.Decided {
				t.Error("first call decided the others before anyone was asked")
			}
			if c.calls == 2 {
				retry := (*calls)[1]
				if !retry.Others.Decided || !slices.Equal(retry.Others.Stop, c.stop) {
					t.Errorf("retry Others = %+v, want Decided, Stop %v", retry.Others, c.stop)
				}
				if retry.AllowReplace {
					t.Error("retry AllowReplace = true: answering about other providers must not grant a same-family replace")
				}
			}
		})
	}
}

// TestDriveStartOthersPromptErrorRetriesNothing verifies a prompt that fails
// (no terminal to open) ends the start with its error and no retry: a failed
// question is not an answer. Retrying on it would load the model beside, or
// stop, other providers' models the user was never able to answer about, and
// would hide why nothing was asked (the error names --others).
func TestDriveStartOthersPromptErrorRetriesNothing(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "")
	stubChooseOthers(t, othersCancel, errors.New("no tty"))
	calls := stubOthersEngine(t, othersFixture())

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err == nil || err.Error() != "no tty" {
		t.Fatalf("err = %v, want the prompt's error", err)
	}
	if len(*calls) != 1 {
		t.Errorf("engine calls = %d, want 1", len(*calls))
	}
}

// TestDriveStartOthersPromptDropsStopWhenNothingIsStoppable verifies the
// question offers no stop when every resident is one wt cannot stop or a
// family it cannot read, and names the unreadable family with its address. An
// offered stop that cannot happen would read as a promise wt breaks.
func TestDriveStartOthersPromptDropsStopWhenNothingIsStoppable(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "")
	stubSessionCounts(t, nil)
	asked := stubChooseOthers(t, othersKeep, nil)
	occ := &lifecycle.OccupiedError{
		Others:        []lifecycle.Resident{othersFixture().Others[2]},
		OthersUnknown: []string{"mtplx"},
	}
	calls := stubOthersEngine(t, occ)

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := "starting omlx/qwen3.8 loads it beside models of other providers:\n" +
		"  mlx_lm_server/p — memory use unknown; wt cannot stop it\n" +
		"  cannot tell whether mtplx at http://localhost:8003 is serving a model\n" +
		"keep them and start, or cancel? [k/N]"
	if len(*asked) != 1 || (*asked)[0].question != want || (*asked)[0].canStop {
		t.Errorf("asked = %+v, want one question without stop:\n%s", *asked, want)
	}
	if len(*calls) != 2 || !(*calls)[1].Others.Decided || len((*calls)[1].Others.Stop) != 0 {
		t.Errorf("calls = %+v, want a retry that keeps everything", *calls)
	}
}

// TestDriveStartSameFamilyAndOthersAskTwiceRetryOnce verifies a start that
// would replace a model of its own family and load beside other providers'
// asks the y/N first, then the others question, and retries once with both
// answers. Two retries would mean two refusals' worth of engine work; a
// missing question would decide for the user.
func TestDriveStartSameFamilyAndOthersAskTwiceRetryOnce(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "")
	stubSessionCounts(t, nil)
	var order []string
	oldConfirm := confirmReplace
	confirmReplace = func(q string) (bool, error) { order = append(order, "replace"); return true, nil }
	t.Cleanup(func() { confirmReplace = oldConfirm })
	oldChoose := chooseOthers
	chooseOthers = func(string, bool) (othersChoice, error) { order = append(order, "others"); return othersStop, nil }
	t.Cleanup(func() { chooseOthers = oldChoose })
	occ := othersFixture()
	occ.Occupants = []localmodels.Entry{localmodelsEntry("omlx/other", "other")}
	calls := stubOthersEngine(t, occ)

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if strings.Join(order, ",") != "replace,others" {
		t.Errorf("questions = %v, want replace then others", order)
	}
	if len(*calls) != 2 {
		t.Fatalf("engine calls = %d, want 2", len(*calls))
	}
	r := (*calls)[1]
	if !r.AllowReplace || !r.Others.Decided || len(r.Others.Stop) != 2 {
		t.Errorf("retry = AllowReplace %v Others %+v, want both answers", r.AllowReplace, r.Others)
	}
}

// TestDriveStartReplaceFlagStillAsksAboutOthers pins that --replace answers
// only the same-family question: the others are still asked about, and
// keeping them stops nothing. --replace implying a stop of another provider's
// model would cut off a session the user never chose to end.
func TestDriveStartReplaceFlagStillAsksAboutOthers(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "")
	stubSessionCounts(t, nil)
	asked := stubChooseOthers(t, othersKeep, nil)
	occ := othersFixture()
	occ.Occupants = []localmodels.Entry{localmodelsEntry("omlx/other", "other")}
	calls := stubOthersEngine(t, occ)

	if err := startForLaunch(&config.Config{}, startTestRow(), true); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(*asked) != 1 {
		t.Errorf("others asked %d times, want 1", len(*asked))
	}
	r := (*calls)[len(*calls)-1]
	if !r.AllowReplace || !r.Others.Decided || len(r.Others.Stop) != 0 {
		t.Errorf("retry = AllowReplace %v Others %+v, want replace and keep", r.AllowReplace, r.Others)
	}
}

// TestDriveStartWithoutTTYKeepsOthersAndWarnsOnce pins the no-TTY rule
// (#412): with no --others, a scripted start keeps the other providers'
// models, prints exactly one warning line naming them with their figures and
// the flag, never asks and never stops. Asking would hang a script; stopping
// would end a session nobody approved ending.
func TestDriveStartWithoutTTYKeepsOthersAndWarnsOnce(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, false)
	stubOthersMode(t, "")
	asked := stubChooseOthers(t, othersStop, nil)
	stderr := captureStderr(t)
	occ := othersFixture()
	occ.OthersUnknown = []string{"mtplx"}
	calls := stubOthersEngine(t, occ)

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := "wt: warning: starting omlx/qwen3.8 beside mtplx/qwen3.8-35b (memory use unknown), " +
		"ollama/gemma4:12b (9.6 GB loaded), mlx_lm_server/p (memory use unknown; wt cannot stop it), " +
		"whatever mtplx at http://localhost:8003 is serving; " +
		"pass --others=stop to stop them, --others=keep to silence this\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want exactly %q", got, want)
	}
	if len(*asked) != 0 {
		t.Error("asked without a TTY")
	}
	if len(*calls) != 2 || !(*calls)[1].Others.Decided || len((*calls)[1].Others.Stop) != 0 {
		t.Errorf("calls = %+v, want a retry that keeps everything", *calls)
	}
}

// TestDriveStartWithoutTTYAdvisesStopOnlyWhenSomethingIsStoppable pins the
// advice half of that line (#412): a resident wt cannot stop (an mlx_lm_server
// pairing, an omlx model still loading) or an unreadable provider is kept with
// the line, but the line must not tell a script to pass --others=stop, which
// would stop nothing and leave the script believing it had made room.
func TestDriveStartWithoutTTYAdvisesStopOnlyWhenSomethingIsStoppable(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, false)
	stubOthersMode(t, "")
	asked := stubChooseOthers(t, othersStop, nil)
	stderr := captureStderr(t)
	occ := &lifecycle.OccupiedError{
		Others: []lifecycle.Resident{
			{Entry: localmodels.Entry{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", ModelName: "p"}},
			{Entry: localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/loading", ModelName: "loading", Loading: true}},
		},
		OthersUnknown: []string{"mtplx"},
	}
	calls := stubOthersEngine(t, occ)

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := "wt: warning: starting omlx/qwen3.8 beside mlx_lm_server/p (memory use unknown; wt cannot stop it), " +
		"omlx/loading (loading) (memory use unknown; wt cannot stop it), " +
		"whatever mtplx at http://localhost:8003 is serving; " +
		"pass --others=keep to silence this\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want exactly %q", got, want)
	}
	if len(*asked) != 0 {
		t.Error("asked without a TTY")
	}
	if len(*calls) != 2 || !(*calls)[1].Others.Decided || len((*calls)[1].Others.Stop) != 0 {
		t.Errorf("calls = %+v, want a retry that keeps everything", *calls)
	}
}

// TestDriveStartOthersStopStopsOnlyStoppable pins --others=stop without a
// TTY: the retry stops every resident wt can stop and nothing else (not the
// mlx_lm_server pairing), without asking and without the warning. A wider
// Stop list would have the engine try to stop a model wt has no way to stop,
// or one the refusal never named; a question or a warning would hang or
// clutter the script that passed the flag to answer it.
func TestDriveStartOthersStopStopsOnlyStoppable(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, false)
	stubOthersMode(t, "stop")
	asked := stubChooseOthers(t, othersKeep, nil)
	stderr := captureStderr(t)
	calls := stubOthersEngine(t, othersFixture())

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(*asked) != 0 {
		t.Error("asked although --others answered")
	}
	if strings.Contains(stderr.String(), "warning") {
		t.Errorf("stderr = %q, want no warning", stderr.String())
	}
	want := []string{"mtplx/qwen3.8-35b", "ollama/gemma4:12b"}
	if len(*calls) != 2 || !slices.Equal((*calls)[1].Others.Stop, want) {
		t.Errorf("calls = %+v, want a retry stopping %v", *calls, want)
	}
}

// TestDriveStartOthersKeepAnswersUnasked pins --others=keep: the very first
// engine call has decided to keep, so there is no refusal, no question and no
// warning. A keep that still warned would make the flag useless for silencing
// a script.
func TestDriveStartOthersKeepAnswersUnasked(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, false)
	stubOthersMode(t, "keep")
	asked := stubChooseOthers(t, othersStop, nil)
	stderr := captureStderr(t)
	calls := stubOthersEngine(t, othersFixture())

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(*calls) != 1 || !(*calls)[0].Others.Decided || len((*calls)[0].Others.Stop) != 0 {
		t.Errorf("calls = %+v, want one call that keeps", *calls)
	}
	if len(*asked) != 0 || stderr.String() != "" {
		t.Errorf("asked %d, stderr %q, want neither", len(*asked), stderr.String())
	}
}

// TestDriveStartUnknownOccupancyThenOthers verifies a same-family
// occupancy wt cannot read, confirmed, followed by a refusal about other
// providers' models gets the others question too, and the start goes ahead
// with both answers. The engine reports the two one after the other, and
// failing the start on the second would read as an error nobody made.
func TestDriveStartUnknownOccupancyThenOthers(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "")
	stubSessionCounts(t, nil)
	oldConfirm := confirmReplace
	confirmReplace = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmReplace = oldConfirm })
	asked := stubChooseOthers(t, othersKeep, nil)
	var calls []lifecycle.Options
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		calls = append(calls, opts)
		switch {
		case !opts.AllowReplace:
			return &lifecycle.OccupancyUnknownError{ProviderID: "omlx", Origin: "http://localhost:8000"}
		case !opts.Others.Decided:
			return othersFixture()
		}
		return nil
	}
	t.Cleanup(func() { lifecycleStart = old })

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(*asked) != 1 || len(calls) != 3 {
		t.Fatalf("others asked %d, engine calls %d; want 1 and 3", len(*asked), len(calls))
	}
	if last := calls[2]; !last.AllowReplace || !last.Others.Decided {
		t.Errorf("last call = %+v, want both answers", last)
	}
}

// TestDriveStartCtrlCAbortsOthersPrompt verifies Ctrl+C at the others prompt
// ends the start at once, as at the y/N: the terminal read cannot be
// interrupted, and without the race the user would need Enter, then Ctrl+C
// again.
func TestDriveStartCtrlCAbortsOthersPrompt(t *testing.T) {
	cancel := stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "")
	stubSessionCounts(t, nil)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	old := chooseOthers
	chooseOthers = func(string, bool) (othersChoice, error) {
		cancel()
		<-release
		return othersStop, nil
	}
	t.Cleanup(func() { chooseOthers = old })
	calls := stubOthersEngine(t, othersFixture())

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "cancelled before omlx/qwen3.8") {
		t.Fatalf("err = %v, want a cancellation naming the model", err)
	}
	if len(*calls) != 1 {
		t.Errorf("engine calls = %d, want 1", len(*calls))
	}
}

// TestSlotStartOthersAnswersCarryTheOwedRestart pins the slot-profile half
// (startForLaunchOwed): a cancelled others question leaves the owed restart
// with the caller (stillOwed, so the written cloud routes get published),
// and a keep or stop retry carries RestartOwed so the start's one restart
// publishes them. A lost flag leaves the cloud slots unrouted.
func TestSlotStartOthersAnswersCarryTheOwedRestart(t *testing.T) {
	for _, choice := range []othersChoice{othersCancel, othersKeep, othersStop} {
		t.Run(choice.String(), func(t *testing.T) {
			stubSignals(t)
			stubStdinTTY(t, true)
			stubOthersMode(t, "")
			stubSessionCounts(t, nil)
			stubChooseOthers(t, choice, nil)
			calls := stubOthersEngine(t, othersFixture())

			still, err := startForLaunchOwed(&config.Config{}, startTestRow(), false, true)
			if choice == othersCancel {
				if err == nil || !still {
					t.Errorf("cancel: still = %v err = %v, want the restart still owed and an error", still, err)
				}
				return
			}
			if err != nil || still {
				t.Errorf("still = %v err = %v, want a started model and nothing owed", still, err)
			}
			for i, o := range *calls {
				if !o.RestartOwed {
					t.Errorf("call %d RestartOwed = false", i)
				}
			}
		})
	}
}

// TestOthersFlagReachesEveryStartingCommand verifies --others is a root
// flag that `wt start`, `wt smoke` and the root launch (-M) all parse into
// the one mode the start driver reads, and that a value other than keep or
// stop is refused before anything runs. A command that dropped the flag
// would ask (or warn) although the user answered.
func TestOthersFlagReachesEveryStartingCommand(t *testing.T) {
	stubOthersMode(t, "")
	for _, args := range [][]string{{"start", "--others=stop"}, {"smoke", "--others=stop"}, {"--others=stop"}} {
		root := rootCmd()
		cmd, rest, err := root.Find(args)
		if err != nil {
			t.Fatalf("%v: find: %v", args, err)
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatalf("%v: parse: %v", args, err)
		}
		if othersMode != "stop" {
			t.Errorf("%v: othersMode = %q, want stop", args, othersMode)
		}
	}
	out, err := runWT(t, "start", "--others=maybe")
	if err == nil || !strings.Contains(err.Error()+out, `"maybe"`) || !strings.Contains(err.Error()+out, "keep or stop") {
		t.Errorf("err = %v out = %q, want a usage error naming the value and keep or stop", err, out)
	}
}

// TestSmokeWithoutTTYKeepsOthersAndWarns runs `wt smoke --json` through the
// real start driver with no TTY: the start keeps the other providers' models,
// stderr gets the one warning line, and the rows run. `wt smoke` in CI must
// neither hang on a prompt nor stop a model another session is using.
func TestSmokeWithoutTTYKeepsOthersAndWarns(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubSignals(t)
	stubOthersMode(t, "")
	stderr := captureStderr(t)
	calls := stubOthersEngine(t, othersFixture())
	oldStart, oldRel, oldPick, oldTTY, oldExit := startModel, releaseSession, runStopPicker, stdinTTY, smokeExit
	t.Cleanup(func() {
		startModel, releaseSession, runStopPicker, stdinTTY, smokeExit = oldStart, oldRel, oldPick, oldTTY, oldExit
	})
	startModel = startForLaunch
	releaseSession = func() {}
	runStopPicker = func(*config.Config) {}
	stdinTTY = func() bool { return false }
	smokeExit = func(int) {}
	t.Cleanup(smoke.SetBuildAndRunForTest(func(_ *config.Config, _ string, _ config.Model, _ string, _ string, _ time.Duration, _ smoke.ProfileApplier, cleanup *func() error) smoke.ExecOutcome {
		*cleanup = func() error { return nil }
		return smoke.StubOutcome("ok", 0)
	}))

	if _, _, err := runSmokeCmd(&app{cfg: cfg}, "ollama/not-eligible:x", "--json"); err != nil {
		t.Fatalf("smoke: %v", err)
	}
	if n := strings.Count(stderr.String(), "wt: warning: starting "); n != 1 {
		t.Errorf("stderr = %q, want one warning line", stderr.String())
	}
	if len(*calls) != 2 || !(*calls)[1].Others.Decided || len((*calls)[1].Others.Stop) != 0 {
		t.Errorf("calls = %+v, want a retry that keeps everything", *calls)
	}
}

// TestDriveStartUnknownOccupancyReplacingLineWaitsForTheOthers pins when the
// "replacing whatever <provider> is serving" line of a confirmed unknown
// occupancy prints: once, when the engine goes ahead with the start, and
// never when the others question that follows is cancelled. Printed before
// the others answer, a cancel would leave the user told something was
// replaced although wt replaced nothing.
func TestDriveStartUnknownOccupancyReplacingLineWaitsForTheOthers(t *testing.T) {
	for _, c := range []struct {
		choice othersChoice
		want   int
	}{{othersCancel, 0}, {othersKeep, 1}} {
		t.Run(c.choice.String(), func(t *testing.T) {
			stubSignals(t)
			stubStdinTTY(t, true)
			stubOthersMode(t, "")
			stubSessionCounts(t, nil)
			oldConfirm := confirmReplace
			confirmReplace = func(string) (bool, error) { return true, nil }
			t.Cleanup(func() { confirmReplace = oldConfirm })
			stubChooseOthers(t, c.choice, nil)
			stderr := captureStderr(t)
			old := lifecycleStart
			lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
				switch {
				case !opts.AllowReplace:
					return &lifecycle.OccupancyUnknownError{ProviderID: "omlx", Origin: "http://localhost:8000"}
				case !opts.Others.Decided:
					return othersFixture()
				}
				opts.Progress(lifecycle.StageWarming)
				return nil
			}
			t.Cleanup(func() { lifecycleStart = old })

			err := startForLaunch(&config.Config{}, startTestRow(), false)
			if (c.choice == othersCancel) != (err != nil) {
				t.Fatalf("err = %v", err)
			}
			line := "wt: replacing whatever omlx is serving at http://localhost:8000\n"
			if n := strings.Count(stderr.String(), line); n != c.want {
				t.Errorf("stderr = %q, want the replacing line %d time(s)", stderr.String(), c.want)
			}
		})
	}
}

// TestSlotStartRefusalOfAnsweredQuestionsLeavesRestartOwed pins that a
// refusal asking only what was already answered (the engine cannot produce
// one today; a future engine might) still reports the slot profile's restart
// as owed: the refusal touched nothing, so the engine did not bounce the
// proxy, and a false stillOwed would leave the profile's cloud routes
// written but never published.
func TestSlotStartRefusalOfAnsweredQuestionsLeavesRestartOwed(t *testing.T) {
	stubSignals(t)
	stubStdinTTY(t, true)
	stubOthersMode(t, "keep")
	captureStderr(t)
	old := lifecycleStart
	lifecycleStart = func(context.Context, *config.Config, lifecycle.Target, lifecycle.Options) error {
		return &lifecycle.OccupiedError{Occupants: []localmodels.Entry{localmodelsEntry("omlx/other", "other")}}
	}
	t.Cleanup(func() { lifecycleStart = old })

	still, err := startForLaunchOwed(&config.Config{}, startTestRow(), true, true)
	if err == nil || !still {
		t.Errorf("still = %v err = %v, want an error with the restart still owed", still, err)
	}
}
