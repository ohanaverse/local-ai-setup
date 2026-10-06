package survey

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestStopPickerSingleModelFamilyBusyBlocksSiblings verifies that when one
// running model of a single-model provider (mtplx: one model per process) is
// in use by another session, a sibling row that reads as running is not
// offered either. Stopping the sibling runs a whole-provider stop, which would
// kill the server under the other session. Multi-tenant ollama is unaffected.
func TestStopPickerSingleModelFamilyBusyBlocksSiblings(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("mtplx", "mtplx/m-4bit", "m-4bit"),
			runningEntry("mtplx", "mtplx/m-6bit", "m-6bit"),
			runningEntry("ollama", "ollama/a", "a"),
		}},
		counts: map[string]int{"mtplx/m-4bit": 1},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 || h.stops[0] != "ollama|a" {
		t.Fatalf("stops = %v, want only the ollama model", h.stops)
	}
	if strings.Contains(out.String(), "6bit") {
		t.Errorf("output offers the sibling of a busy variant: %q", out.String())
	}
}

// TestStopPickerPoolSiblingsAreIndependent pins #213 for the stop picker: omlx
// holds a pool, and stopping one model only unloads it. So an idle omlx model
// is offered while another session uses its sibling, and two selected omlx
// models are each stopped — skipping the second, as for a single-model
// provider, would print "done" for a model that is still loaded.
func TestStopPickerPoolSiblingsAreIndependent(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("omlx", "omlx/m-4bit", "m-4bit"),
			runningEntry("omlx-6bit", "omlx-6bit/m-6bit", "m-6bit"),
			runningEntry("omlx", "omlx/n-4bit", "n-4bit"),
		}},
		counts: map[string]int{"omlx/m-4bit": 1},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 2 || h.stops[0] != "omlx-6bit|m-6bit" || h.stops[1] != "omlx|n-4bit" {
		t.Fatalf("stops = %v, want both idle omlx models stopped, each by its own stop, and the busy one left", h.stops)
	}
	if strings.Contains(out.String(), "omlx/m-4bit") {
		t.Errorf("output offers the model another session is using: %q", out.String())
	}
}

// TestStopPickerSingleModelFamilyStoppedOnce verifies selecting two idle
// rows of one single-model provider (mtplx) runs the provider stop once and
// reports both done, since the first stop already took the server down.
func TestStopPickerSingleModelFamilyStoppedOnce(t *testing.T) {
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
		runningEntry("mtplx", "mtplx/m-4bit", "m-4bit"),
		runningEntry("mtplx", "mtplx/m-6bit", "m-6bit"),
	}}}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 {
		t.Fatalf("stops = %v, want exactly one provider stop", h.stops)
	}
	if strings.Count(out.String(), "... done") != 2 {
		t.Errorf("output = %q, want two done lines", out.String())
	}
}

// TestStopPickerAliasRowsShareUsage verifies that two registry rows naming the
// same provider-side model ("qwen3.8" and ollama's implicit "qwen3.8:latest")
// share one usage count: a live session launched from row A keeps row B off
// the list. Counting by registry id alone offered idle alias B, and
// `ollama stop` on it unloaded the single loaded copy under the live session.
func TestStopPickerAliasRowsShareUsage(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/qwen", "qwen3.8"),
			runningEntry("ollama", "ollama/qwen-latest", "qwen3.8:latest"),
			runningEntry("ollama", "ollama/other", "other"),
		}},
		counts: map[string]int{"ollama/qwen": 1},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 || h.stops[0] != "ollama|other" {
		t.Fatalf("stops = %v, want only ollama|other (both qwen aliases are in use)", h.stops)
	}
}
