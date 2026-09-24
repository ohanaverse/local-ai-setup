// wt/internal/profiles/merge_test.go
package profiles

import (
	"reflect"
	"testing"
)

// TestMergeIntoDeepMergesNestedMaps verifies mergeInto recurses into a
// nested map[string]any shared by both sides instead of replacing it
// wholesale — the core behavior findings #6 and #7 depend on: a profile
// adding one nested field to an object (e.g. config_content.env.SOME_KEY)
// must not drop that object's other existing fields.
func TestMergeIntoDeepMergesNestedMaps(t *testing.T) {
	dst := map[string]any{
		"provider": map[string]any{"baseURL": "http://x", "apiKey": "k"},
		"other":    "untouched",
	}
	src := map[string]any{
		"provider": map[string]any{"models": []any{"m1"}},
	}
	mergeInto(dst, src)
	want := map[string]any{
		"provider": map[string]any{"baseURL": "http://x", "apiKey": "k", "models": []any{"m1"}},
		"other":    "untouched",
	}
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("mergeInto() dst = %#v, want %#v", dst, want)
	}
}

// TestMergeIntoReplacesNonMapValues verifies a scalar or list value on
// either side is whole-value replacement (not merged), matching TOML's own
// "last value wins" semantics — only map[string]any values recurse.
func TestMergeIntoReplacesNonMapValues(t *testing.T) {
	dst := map[string]any{"model": "old", "tags": []any{"a"}}
	src := map[string]any{"model": "new", "tags": []any{"b", "c"}}
	mergeInto(dst, src)
	if dst["model"] != "new" {
		t.Errorf(`dst["model"] = %v, want "new"`, dst["model"])
	}
	if !reflect.DeepEqual(dst["tags"], []any{"b", "c"}) {
		t.Errorf(`dst["tags"] = %v, want [b c] (whole-list replacement)`, dst["tags"])
	}
}

// TestMergeIntoMapReplacesNonMapDstValue verifies that when src's value at
// a key is a map but dst's existing value at that key is NOT a map (a type
// mismatch a hand-edited profiles.toml could produce), the whole value is
// replaced rather than panicking or silently keeping the stale scalar.
func TestMergeIntoMapReplacesNonMapDstValue(t *testing.T) {
	dst := map[string]any{"provider": "not-a-map"}
	src := map[string]any{"provider": map[string]any{"baseURL": "http://x"}}
	mergeInto(dst, src)
	want := map[string]any{"baseURL": "http://x"}
	if !reflect.DeepEqual(dst["provider"], want) {
		t.Errorf(`dst["provider"] = %#v, want %#v`, dst["provider"], want)
	}
}
