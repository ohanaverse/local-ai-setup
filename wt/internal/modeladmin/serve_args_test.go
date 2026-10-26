package modeladmin

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func args(a ...string) *[]string { return &a }

// TestFieldsPatchServeArgs verifies how serve_args become a registry patch
// (#432): given, they are set as the list, one element each and in order; an
// empty list clears the key; not given leaves it alone, and a Fields with only
// serve_args is not empty (so `wt model edit --serve-arg ...` is not refused as
// "nothing to change").
func TestFieldsPatchServeArgs(t *testing.T) {
	set, unset, err := Fields{ServeArgs: args("--profile", "turbo")}.patch()
	if err != nil || !slices.Equal(set["serve_args"].([]string), []string{"--profile", "turbo"}) || len(unset) != 0 {
		t.Errorf("patch = %v, %v, %v; want serve_args set", set, unset, err)
	}
	set, unset, err = Fields{ServeArgs: args()}.patch()
	if err != nil || len(set) != 0 || !slices.Equal(unset, []string{"serve_args"}) {
		t.Errorf("empty list: patch = %v, %v, %v; want serve_args cleared", set, unset, err)
	}
	if set, unset, _ = (Fields{Family: ptr("x")}).patch(); set["serve_args"] != nil || slices.Contains(unset, "serve_args") {
		t.Errorf("not given: patch = %v, %v; want serve_args left alone", set, unset)
	}
	if (Fields{ServeArgs: args("--x")}).Empty() || (Fields{ServeArgs: args()}).Empty() {
		t.Error("a Fields carrying serve_args reads as empty")
	}
	// The patch owns its list: a caller's later change must not reach it.
	given := []string{"--a"}
	set, _, _ = Fields{ServeArgs: &given}.patch()
	given[0] = "--changed"
	if got := set["serve_args"].([]string); got[0] != "--a" {
		t.Errorf("patch shares the caller's slice: %v", got)
	}
}

// TestAddWritesServeArgsForMtplxOnly verifies `wt model add` stores serve_args
// on an mtplx model, at their place in the row (after the tags), and refuses
// them where they cannot be used: a provider that takes none, and a flag wt
// sets itself. Each refusal is a FieldError on --serve-arg raised before
// anything is written, so the registry is untouched.
func TestAddWritesServeArgsForMtplxOnly(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	if _, err := Add(AddRequest{ProviderID: "mtplx", ModelName: "Org/Tuned", Fields: Fields{Family: ptr("qwen3.8"), ServeArgs: args("--profile", "turbo")}}, config.SeedEnv{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	row := got[strings.Index(got, `id = "mtplx/Org/Tuned"`):]
	if !strings.Contains(row, "tags = []\nserve_args = [\n    \"--profile\",\n    \"turbo\",\n]\n") {
		t.Errorf("the mtplx row lacks serve_args after its tags:\n%s", row)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	i := config.IndexModelByID(cfg.Models, "mtplx/Org/Tuned")
	if i < 0 || !slices.Equal(cfg.Models[i].ServeArgs.Args, []string{"--profile", "turbo"}) {
		t.Fatalf("read back %+v", cfg.Models)
	}

	before := read(t, path)
	var fe *FieldError
	for name, req := range map[string]AddRequest{
		"ollama":     {ProviderID: "ollama", ModelName: "q:1", Fields: Fields{Family: ptr("q"), ServeArgs: args("--x")}},
		"owned flag": {ProviderID: "mtplx", ModelName: "Org/Other", Fields: Fields{Family: ptr("q"), ServeArgs: args("--port", "9")}},
	} {
		if _, err := Add(req, config.SeedEnv{}); !errors.As(err, &fe) || fe.Field != FieldServeArg {
			t.Errorf("%s: err = %v, want a FieldError on %s", name, err, FieldServeArg)
		}
		if read(t, path) != before {
			t.Errorf("%s: a refused add changed the registry", name)
		}
	}
}

// TestEditSetsAndClearsServeArgs verifies `wt model edit` on an mtplx row sets
// serve_args, leaves the rest of the row alone, does nothing when the same
// list is given again, replaces a different list, and clears it with an empty
// one; and that it refuses them on a model of a provider that takes none and
// a flag wt sets itself, by name and with the registry untouched.
func TestEditSetsAndClearsServeArgs(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	if _, err := Add(AddRequest{ProviderID: "mtplx", ModelName: "Org/Tuned", Fields: Fields{Family: ptr("qwen3.8"), Tags: ptr("code")}}, config.SeedEnv{}); err != nil {
		t.Fatal(err)
	}
	plain := read(t, path)

	changed, err := Edit("mtplx/Org/Tuned", Fields{ServeArgs: args("--profile", "turbo")})
	if err != nil || !changed {
		t.Fatalf("Edit = (%v, %v), want (true, nil)", changed, err)
	}
	withArgs := read(t, path)
	// The mtplx row is the last in the file: add serve_args after its tags.
	tags := "tags = [\n    \"code\",\n]\n"
	at := strings.LastIndex(plain, tags) + len(tags)
	if want := plain[:at] + "serve_args = [\n    \"--profile\",\n    \"turbo\",\n]\n" + plain[at:]; withArgs != want {
		t.Errorf("registry after the edit =\n%s\nwant only serve_args added:\n%s", withArgs, want)
	}
	if changed, err = Edit("mtplx/Org/Tuned", Fields{ServeArgs: args("--profile", "turbo")}); err != nil || changed {
		t.Errorf("the same list again = (%v, %v), want (false, nil)", changed, err)
	}
	if changed, err = Edit("mtplx/Org/Tuned", Fields{ServeArgs: args("--profile", "stable")}); err != nil || !changed || !strings.Contains(read(t, path), `"stable"`) || strings.Contains(read(t, path), `"turbo"`) {
		t.Errorf("replacing the list = (%v, %v):\n%s", changed, err, read(t, path))
	}
	if changed, err = Edit("mtplx/Org/Tuned", Fields{ServeArgs: args()}); err != nil || !changed || read(t, path) != plain {
		t.Errorf("clearing = (%v, %v); want the row as it was before:\n%s", changed, err, read(t, path))
	}

	var fe *FieldError
	for name, tc := range map[string]struct {
		id string
		a  *[]string
	}{
		"a provider with none": {"ollama/gemma4:9b", args("--x")},
		"an owned flag":        {"mtplx/Org/Tuned", args("--model-id=x")},
		"an empty argument":    {"mtplx/Org/Tuned", args("")},
	} {
		if _, err := Edit(tc.id, Fields{ServeArgs: tc.a}); !errors.As(err, &fe) || fe.Field != FieldServeArg {
			t.Errorf("%s: err = %v, want a FieldError on %s", name, err, FieldServeArg)
		}
		if read(t, path) != plain {
			t.Errorf("%s: a refused edit changed the registry", name)
		}
	}
}
