package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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

// othersAsk is one question the others prompt was asked.
type othersAsk struct {
	question string
	canStop  bool
}

// stubOthersMode sets the --others mode for one test.
func stubOthersMode(t *testing.T, mode string) {
	t.Helper()
	old := othersMode
	othersMode = othersFlag(mode)
	t.Cleanup(func() { othersMode = old })
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
