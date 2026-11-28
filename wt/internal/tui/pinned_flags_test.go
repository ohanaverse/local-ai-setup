package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// othersRefusal is a start job's first answer: other providers' models are
// loaded beside the target (two the server can stop), and no same-family
// occupant. Every later attempt fails, so a test reads the retry's request.
func othersRefusal(_ context.Context, n int, _ modelapi.StartRequest, _ modelclient.Progress) (modelapi.JobStatus, error) {
	if n > 1 {
		return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeFailed, Message: "load failed"}}
	}
	return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{
		Code: modelapi.CodeOthers, Message: "other providers' models are loaded",
		Others: []modelapi.Resident{
			{ID: "ollama/a", Label: "ollama/a", Stoppable: true, Figure: "1.0 GB loaded"},
			{ID: "mtplx/b", Label: "mtplx/b", Stoppable: true, Figure: "memory use unknown"},
		},
	}}
}

// TestReplaceAndOthersFlagsAnswerOnlyForThePinnedRow pins the rule that
// --replace and --others are the -M pin's permissions (chooseRemote,
// othersFor): Enter on a row that is not the pin starts it with neither
// allow_replace nor an others answer, and the others question is asked on the
// dialog; the pinned row's start carries allow_replace, and --others=stop
// answers the refusal with every model the server can stop, retrying with no
// dialog. Without it --replace given for one model would silently displace a
// model the user picked by hand.
func TestReplaceAndOthersFlagsAnswerOnlyForThePinnedRow(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog(), startFn: othersRefusal}
	m := remoteTable(t, f)
	m.allowReplace, m.othersMode, m.pinnedModel = true, "stop", "openrouter/glm-5.3"
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	if reqs := waitStarts(t, f, 1); reqs[0].AllowReplace || reqs[0].Others != nil {
		t.Errorf("unpinned start = %+v, want no allow_replace and no others answer", reqs[0])
	}
	m, _ = updateMsg(m, recvStart(t, m))
	if m.phase != phaseReplaceConfirm {
		t.Errorf("phase = %v (status %q), want the others dialog for an unpinned row", m.phase, m.status)
	}

	cat := startRowCatalog()
	g := &fakeModels{cat: cat, resolveRow: cat.Rows[0], startFn: othersRefusal}
	n := remoteModel(t, g, 80, 24)
	n.selectedPath = t.TempDir()
	n.allowReplace, n.othersMode, n.pinnedModel = true, "stop", "omlx/qwen3.8"
	n, cmd := n.enterModelPhaseForAgent()
	n, _ = updateMsg(n, cmd())
	if n.phase != phaseStarting || n.start == nil {
		t.Fatalf("phase = %v: want the pinned row's start", n.phase)
	}
	t.Cleanup(n.start.cancel)
	if reqs := waitStarts(t, g, 1); !reqs[0].AllowReplace {
		t.Errorf("pinned start = %+v, want allow_replace from --replace", reqs[0])
	}
	n, _ = updateMsg(n, recvStart(t, n))
	if n.phase != phaseStarting {
		t.Fatalf("phase = %v (status %q): --others=stop should retry without the dialog", n.phase, n.status)
	}
	reqs := waitStarts(t, g, 2)
	if reqs[1].Others == nil || !slices.Equal(reqs[1].Others.Stop, []string{"ollama/a", "mtplx/b"}) {
		t.Errorf("retry = %+v, want --others=stop's stop list of every stoppable resident", reqs[1])
	}
	n, _ = updateMsg(n, recvStart(t, n))
}

// TestOthersKeepAnswersThePinnedRowsFirstStart pins --others=keep on the
// pinned row: its very first start request already carries the answer (keep,
// stop nothing), so the server never has to ask. An unpinned row never gets
// it.
func TestOthersKeepAnswersThePinnedRowsFirstStart(t *testing.T) {
	cat := startRowCatalog()
	g := &fakeModels{cat: cat, resolveRow: cat.Rows[0], startFn: othersRefusal}
	n := remoteModel(t, g, 80, 24)
	n.selectedPath = t.TempDir()
	n.othersMode, n.pinnedModel = "keep", "omlx/qwen3.8"
	n, cmd := n.enterModelPhaseForAgent()
	n, _ = updateMsg(n, cmd())
	if n.start == nil {
		t.Fatalf("phase = %v: want the pinned row's start", n.phase)
	}
	t.Cleanup(n.start.cancel)
	if reqs := waitStarts(t, g, 1); reqs[0].AllowReplace || reqs[0].Others == nil || len(reqs[0].Others.Stop) != 0 {
		t.Errorf("pinned start = %+v, want a keep answer (stop nothing) and no allow_replace", reqs[0])
	}
}

// TestSlotStartTakesTheFlagsOnlyWhenTheProfileIsThePin is the slot twin
// (handleRemoteSlotPrepared): the start of a profile's local slot model
// carries --replace and --others only when that profile is the -M pin.
func TestSlotStartTakesTheFlagsOnlyWhenTheProfileIsThePin(t *testing.T) {
	slots := map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/idle"}
	for _, pinned := range []bool{false, true} {
		m, f := slotRemoteTable(t, slots)
		m.allowReplace, m.othersMode = true, "keep"
		if pinned {
			m.pinnedModel = "profile/mix"
		}
		driveSlotLaunch(t, m)
		reqs := waitStarts(t, f, 1)
		if got := reqs[0].AllowReplace && reqs[0].Others != nil; got != pinned {
			t.Errorf("pinned=%v: start = %+v, want the flags only for the pinned profile", pinned, reqs[0])
		}
		if !pinned && (reqs[0].AllowReplace || reqs[0].Others != nil) {
			t.Errorf("unpinned profile's start carried a flag: %+v", reqs[0])
		}
	}
}

// TestALaunchThatNeverCommitsRecordsNothing pins that the client picker
// records a launch only at its commit point: Enter on a launch row starts the
// prep and records nothing, and esc out of the launching screen (the prep
// called off) leaves last-picked.state and usage.jsonl unwritten (and
// rotation.state, which nothing writes), as does a prep that fails. Without
// it the marker would move to a model the user never ran.
func TestALaunchThatNeverCommitsRecordsNothing(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	m := remoteTable(t, f)
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt")
	unrecorded := func(when string) {
		t.Helper()
		for _, name := range []string{"rotation.state", "last-picked.state", "usage.jsonl"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				t.Errorf("%s: %s was written before any launch committed", when, name)
			}
		}
	}
	m, cmd := enterRemote(t, m, "openrouter/glm-5.3")
	if m.phase != phaseLaunching || cmd == nil {
		t.Fatalf("phase = %v, status %q: want the launching screen", m.phase, m.status)
	}
	unrecorded("after Enter")
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != phaseModel {
		t.Fatalf("phase = %v after esc, want the table", m.phase)
	}
	unrecorded("after esc")
	m, cmd = enterRemote(t, m, "openrouter/glm-5.3")
	m, _ = updateMsg(m, remotePreparedMsg{id: m.prep.id, modelID: "openrouter/glm-5.3", err: errors.New("prep failed")})
	if m.phase != phaseModel || !strings.Contains(m.status, "launch failed") {
		t.Errorf("phase = %v, status %q: want the failure on the table", m.phase, m.status)
	}
	_ = cmd
	unrecorded("after a failed prep")
}
