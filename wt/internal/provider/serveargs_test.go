package provider

import (
	"slices"
	"testing"
)

// TestSpawnArgsAppendsExtraAfterWtsOwn pins the one builder of a spawn argv
// (#432): the template with {model} and {port} replaced comes first, in its
// own order, and the extra arguments follow untouched. With none the argv is
// exactly what the template gave, so a model without serve_args spawns as it
// always did; the template and extra are never changed.
func TestSpawnArgsAppendsExtraAfterWtsOwn(t *testing.T) {
	spec, ok := ProcessFor("mtplx")
	if !ok {
		t.Fatal("no mtplx process spec")
	}
	base := []string{"serve", "--model", "Org/M", "--port", "8003", "--host", "127.0.0.1", "--model-id", "Org/M"}
	if got := spec.SpawnArgs("Org/M", 8003, nil); !slices.Equal(got, base) {
		t.Errorf("SpawnArgs with no extra = %v, want %v", got, base)
	}
	extra := []string{"--profile", "turbo", "--verbose"}
	if got, want := spec.SpawnArgs("Org/M", 8003, extra), append(slices.Clone(base), extra...); !slices.Equal(got, want) {
		t.Errorf("SpawnArgs = %v, want %v", got, want)
	}
	if !slices.Equal(extra, []string{"--profile", "turbo", "--verbose"}) {
		t.Errorf("SpawnArgs changed the extra it was given: %v", extra)
	}
	if again, _ := ProcessFor("mtplx"); !slices.Equal(again.Spawn, spec.Spawn) || slices.Contains(again.Spawn, "--profile") {
		t.Errorf("SpawnArgs changed the table's template: %v", again.Spawn)
	}
}

// TestOwnedFlagsAreDerivedFromTheSpec verifies the flags wt refuses in a
// model's serve_args come from the spec and not from a second list: every
// `--flag` of Spawn (a value such as 127.0.0.1 is not one) plus the spec's
// port and model flags. A spawn template that gains a flag therefore refuses
// it in serve_args with no further edit.
func TestOwnedFlagsAreDerivedFromTheSpec(t *testing.T) {
	spec, _ := ProcessFor("mtplx")
	got := spec.OwnedFlags()
	slices.Sort(got)
	if want := []string{"--host", "--model", "--model-id", "--port"}; !slices.Equal(got, want) {
		t.Errorf("mtplx OwnedFlags = %v, want %v", got, want)
	}
	synthetic := ProcessSpec{Spawn: []string{"run", "--alpha", "{model}", "--beta=1", "plain"}, PortFlag: "--gamma", ModelFlag: "--alpha"}
	got = synthetic.OwnedFlags()
	slices.Sort(got)
	if want := []string{"--alpha", "--beta=1", "--gamma"}; !slices.Equal(got, want) {
		t.Errorf("synthetic OwnedFlags = %v, want %v (each flag once, the spec's own port and model flags included)", got, want)
	}
}

// TestOnlyMtplxAcceptsServeArgs pins which families take serve_args: mtplx,
// whose serve flags are worth choosing per model, and no other, rec (one
// fixed model) and unknown families included.
func TestOnlyMtplxAcceptsServeArgs(t *testing.T) {
	for family, want := range map[string]bool{"mtplx": true, "rec": false, "omlx": false, "ollama": false, "mlx_lm_server": false, "nope": false} {
		if got := AcceptsServeArgs(family); got != want {
			t.Errorf("AcceptsServeArgs(%q) = %v, want %v", family, got, want)
		}
	}
	for _, family := range ProcessFamilies() {
		if AcceptsServeArgs(family) {
			if spec, _ := ProcessFor(family); len(spec.OwnedFlags()) == 0 {
				t.Errorf("%s takes serve_args but owns no flags to refuse", family)
			}
		}
	}
}
