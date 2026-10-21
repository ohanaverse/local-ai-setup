package lifecycle

import (
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func TestStoppableIDs(t *testing.T) {
	res := func(id string, stoppable bool) Resident {
		return Resident{Entry: localmodels.Entry{ModelID: id}, Stoppable: stoppable}
	}
	if got := StoppableIDs(nil); got != nil {
		t.Errorf("no residents: got %v, want nil", got)
	}
	if got := StoppableIDs([]Resident{res("mlx_lm_server/x", false)}); got != nil {
		t.Errorf("none stoppable: got %v, want nil", got)
	}
	got := StoppableIDs([]Resident{res("ollama/a", true), res("mlx_lm_server/x", false), res("omlx/b", true)})
	if want := []string{"ollama/a", "omlx/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (engine order, unstoppable skipped)", got, want)
	}
}
