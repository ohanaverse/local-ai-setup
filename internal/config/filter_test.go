package config

import (
	"reflect"
	"testing"
)

// TestParseFilterList covers the comma-delimited parser used by the -T and
// -F CLI flags. Edge cases here are user-facing: a stray comma or
// whitespace in `wt -T "code, design"` must not produce an empty tag entry
// that silently filters every model out.
func TestParseFilterList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single", "code", []string{"code"}},
		{"two", "code,design", []string{"code", "design"}},
		{"with spaces", " code , design ", []string{"code", "design"}},
		{"trailing comma", "code,", []string{"code"}},
		{"leading comma", ",code", []string{"code"}},
		{"double comma", "code,,design", []string{"code", "design"}},
		{"three", "a,b,c", []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFilterList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseFilterList(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseFilterListExported verifies that ParseFilterList (the exported
// form used by callers outside the config package, e.g. cmd/wt/launch.go)
// behaves identically to the private parseFilterList. Without this, the
// public alias could drift from the implementation and silently break
// launchFiltered's tag-derived rotation slot.
func TestParseFilterListExported(t *testing.T) {
	for _, in := range []string{"", "   ", "code", "code,design", " code , design ", "code,", ",code", "code,,design"} {
		want := parseFilterList(in)
		got := ParseFilterList(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ParseFilterList(%q) = %#v, want %#v", in, got, want)
		}
	}
}
