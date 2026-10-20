// wt/internal/profiles/leaves_test.go
package profiles

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func sortedRecs(recs []leafRec) []leafRec {
	out := append([]leafRec(nil), recs...)
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i].Path, "\x00") < strings.Join(out[j].Path, "\x00")
	})
	return out
}

// TestCollectLeavesRecordsEachLeafPath verifies every scalar under nested
// objects becomes one record with its own path and the hash of its value:
// restore works per leaf, so a record that named a whole object would again
// drop the session's other writes under it (#401).
func TestCollectLeavesRecordsEachLeafPath(t *testing.T) {
	content := map[string]any{
		"permissions": map[string]any{"defaultMode": "default"},
		"env":         map[string]any{"A": "1", "B": "2"},
	}
	got := sortedRecs(collectLeaves(map[string]any{}, content))
	want := []leafRec{
		{Path: []string{"env", "A"}, Hash: hashLeaf("1")},
		{Path: []string{"env", "B"}, Hash: hashLeaf("2")},
		{Path: []string{"permissions", "defaultMode"}, Hash: hashLeaf("default")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectLeaves() = %#v, want %#v", got, want)
	}
}

// TestCollectLeavesTreatsArraysNullAndNumbersAsWholeValues verifies arrays,
// null and numbers are single leaves, as mergeInto replaces them wholesale,
// and that a TOML int64 hashes like the JSON float64 read back from disk,
// so a restore does not mistake its own write for a session edit.
func TestCollectLeavesTreatsArraysNullAndNumbersAsWholeValues(t *testing.T) {
	content := map[string]any{"arr": []any{"a", "b"}, "nul": nil, "num": int64(5)}
	got := sortedRecs(collectLeaves(map[string]any{}, content))
	if len(got) != 3 {
		t.Fatalf("collectLeaves() = %#v, want 3 leaves", got)
	}
	if got[0].Hash != hashLeaf([]any{"a", "b"}) || got[1].Hash != hashLeaf(nil) || got[2].Hash != hashLeaf(int64(5)) {
		t.Errorf("hashes do not match the values: %#v", got)
	}
	if hashLeaf(int64(5)) != hashLeaf(float64(5)) {
		t.Errorf("hashLeaf(int64(5)) != hashLeaf(float64(5))")
	}
}

// TestCollectLeavesEmptyMapIsALeafOnlyWhenItWillBeWritten verifies {} is
// recorded only where mergeInto writes it; merging {} into an existing
// object is a no-op, and recording it would restore (delete) an object the
// user already had.
func TestCollectLeavesEmptyMapIsALeafOnlyWhenItWillBeWritten(t *testing.T) {
	content := map[string]any{"x": map[string]any{}}
	got := collectLeaves(map[string]any{}, content)
	want := []leafRec{{Path: []string{"x"}, Hash: hashLeaf(map[string]any{})}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("over empty doc: got %#v, want %#v", got, want)
	}
	doc := map[string]any{"x": map[string]any{"a": float64(1)}}
	if got := collectLeaves(doc, content); len(got) != 0 {
		t.Errorf("over existing object: got %#v, want none", got)
	}
	doc = map[string]any{"x": "scalar"}
	if got := collectLeaves(doc, content); len(got) != 1 {
		t.Errorf("over non-object: got %#v, want one leaf", got)
	}
}

// TestCollectLeavesKeysWithDotsSlashesAndEmptyNames verifies a key is one
// path segment whatever characters it holds, so a dotted key is never
// split into a nested path that restore would then misaddress.
func TestCollectLeavesKeysWithDotsSlashesAndEmptyNames(t *testing.T) {
	content := map[string]any{"a.b": "1", "c/d": "2", "": "3"}
	got := sortedRecs(collectLeaves(map[string]any{}, content))
	want := []leafRec{
		{Path: []string{""}, Hash: hashLeaf("3")},
		{Path: []string{"a.b"}, Hash: hashLeaf("1")},
		{Path: []string{"c/d"}, Hash: hashLeaf("2")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectLeaves() = %#v, want %#v", got, want)
	}
}

// TestGetPathDistinguishesNullFromAbsent verifies a stored null reads as
// present and a missing key or non-object intermediate as absent: restore
// must tell "the session removed it" from "it is now null".
func TestGetPathDistinguishesNullFromAbsent(t *testing.T) {
	doc := map[string]any{"a": map[string]any{"n": nil, "s": "x"}, "top": "str"}
	if v, ok := getPath(doc, []string{"a", "n"}); !ok || v != nil {
		t.Errorf("null: got (%v, %v), want (nil, true)", v, ok)
	}
	if v, ok := getPath(doc, []string{"a", "s"}); !ok || v != "x" {
		t.Errorf("present: got (%v, %v)", v, ok)
	}
	for _, p := range [][]string{{"a", "missing"}, {"nope"}, {"top", "x"}, {"a", "s", "x"}} {
		if _, ok := getPath(doc, p); ok {
			t.Errorf("getPath(%v) ok = true, want false", p)
		}
	}
}

// TestEncodeDecodeLeafRecsRoundTrip verifies the marker survives encode and
// decode unchanged, paths with odd keys included.
func TestEncodeDecodeLeafRecsRoundTrip(t *testing.T) {
	recs := []leafRec{
		{Path: []string{"env", "A.B"}, Hash: "abc"},
		{Path: []string{""}, Hash: "def"},
	}
	raw, err := encodeLeafRecs(recs)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decodeLeafRecs(raw)
	if !ok || !reflect.DeepEqual(got, recs) {
		t.Errorf("decode = (%#v, %v), want %#v", got, ok, recs)
	}
}

// TestDecodeLeafRecsAcceptsLegacyKeyList verifies a marker written before
// this change (a JSON list of top-level keys) still decodes, as records
// with no hash, so an interrupted session from the old wt still restores.
func TestDecodeLeafRecsAcceptsLegacyKeyList(t *testing.T) {
	got, ok := decodeLeafRecs([]byte(`["env","permissions"]`))
	want := []leafRec{{Path: []string{"env"}}, {Path: []string{"permissions"}}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("decode = (%#v, %v), want %#v", got, ok, want)
	}
}

// TestDecodeLeafRecsRejectsGarbage verifies an unreadable marker is
// reported, never guessed at: restoring from a bad marker could delete keys.
func TestDecodeLeafRecsRejectsGarbage(t *testing.T) {
	for _, raw := range []string{``, `not json`, `{"a":1}`, `[1,2]`, `"x"`, `[{"path":"a"}]`} {
		if got, ok := decodeLeafRecs([]byte(raw)); ok {
			t.Errorf("decodeLeafRecs(%q) = (%#v, true), want false", raw, got)
		}
	}
}

// revertCase merges content into a copy of orig (nil: no file), lets
// session edit the result, reverts it and returns what is left.
func revertCase(t *testing.T, orig, content map[string]any, session func(map[string]any)) map[string]any {
	t.Helper()
	cur := map[string]any{}
	if orig != nil {
		cur = deepCopy(orig).(map[string]any)
	}
	recs := collectLeaves(cur, content)
	mergeInto(cur, deepCopy(content).(map[string]any))
	if session != nil {
		session(cur)
	}
	revertLeaves(cur, orig, recs)
	return cur
}

func deepCopy(v any) any {
	if m, ok := v.(map[string]any); ok {
		out := map[string]any{}
		for k, sub := range m {
			out[k] = deepCopy(sub)
		}
		return out
	}
	return v
}

// TestRevertLeavesTypeConflictStopsAtTheFirstNonObject verifies a profile
// subtree written two levels under an original scalar is unwound level by
// level: the emptied inner object the merge created is deleted (the
// original had nothing there), and the scalar comes back at the level the
// original held it. Keeping either object would leave the profile's
// structure behind after cleanup.
func TestRevertLeavesTypeConflictStopsAtTheFirstNonObject(t *testing.T) {
	orig := map[string]any{"a": "x", "keep": 1.0}
	content := map[string]any{"a": map[string]any{"b": map[string]any{"c": "pinned"}}}
	got := revertCase(t, orig, content, nil)
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("reverted = %v, want the original %v", got, orig)
	}
}

// TestRevertLeavesKeysWithDotsSlashesAndEmptyNames verifies a leaf whose
// key holds a dot, a slash or nothing at all is reverted by its own path
// segment: a dotted-string path would confuse "a.b" with a under b.
func TestRevertLeavesKeysWithDotsSlashesAndEmptyNames(t *testing.T) {
	orig := map[string]any{"a.b": "orig", "a": map[string]any{"b": "other"}}
	content := map[string]any{"a.b": "new", "x/y": map[string]any{"": 1.0}}
	got := revertCase(t, orig, content, nil)
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("reverted = %v, want the original %v", got, orig)
	}
}

// TestRevertLeavesLegacyRecordRestoresWithoutTheHashCheck verifies a
// hashless record (an older wt's top-level key) restores the original value
// even though the session changed it: an older marker carries no hash to
// compare with, so it skips that check (a key the session deleted is still
// not brought back).
func TestRevertLeavesLegacyRecordRestoresWithoutTheHashCheck(t *testing.T) {
	cur := map[string]any{"permissions": map[string]any{"defaultMode": "acceptEdits"}, "model": "s"}
	orig := map[string]any{"permissions": map[string]any{"defaultMode": "plan"}}
	revertLeaves(cur, orig, []leafRec{{Path: []string{"permissions"}}, {Path: []string{"env"}}})
	want := map[string]any{"permissions": map[string]any{"defaultMode": "plan"}, "model": "s"}
	if !reflect.DeepEqual(cur, want) {
		t.Errorf("reverted = %v, want %v", cur, want)
	}
}

// TestRevertLeavesSkipsALeafUnderAContainerTheSessionReplaced verifies a
// record whose path now runs through a non-object (the session replaced the
// container) is left alone: the session's value wins.
func TestRevertLeavesSkipsALeafUnderAContainerTheSessionReplaced(t *testing.T) {
	orig := map[string]any{"permissions": map[string]any{"defaultMode": "plan"}}
	got := revertCase(t, orig, map[string]any{"permissions": map[string]any{"defaultMode": "default"}},
		func(cur map[string]any) { cur["permissions"] = "session" })
	if want := map[string]any{"permissions": "session"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reverted = %v, want %v", got, want)
	}
}

// TestOlderWtCannotDecodeTheLeafMarker records what a wt from before #401
// does with today's marker: it decoded .keys as a list of key names, which
// a list of records is not, so it falls back to the whole-file restore (or
// delete) — the session's own writes are lost there, but nothing is guessed
// from a marker it cannot read.
func TestOlderWtCannotDecodeTheLeafMarker(t *testing.T) {
	raw, err := encodeLeafRecs([]leafRec{{Path: []string{"permissions", "defaultMode"}, Hash: hashLeaf("default")}})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err == nil {
		t.Errorf("an older wt decodes %s as key names %v, want a decode error (its whole-file fallback)", raw, keys)
	}
}

func absentDefaults() map[string]any {
	return map[string]any{"permissions": map[string]any{"defaultMode": "default"}}
}

func TestCollectAbsentLeavesSkipsExistingValues(t *testing.T) {
	doc := map[string]any{"permissions": map[string]any{"defaultMode": "auto"}}
	if got := collectAbsentLeaves(doc, absentDefaults()); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestCollectAbsentLeavesSkipsAScalarAncestor(t *testing.T) {
	doc := map[string]any{"permissions": "strict"}
	if got := collectAbsentLeaves(doc, absentDefaults()); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestCollectAbsentLeavesSkipsAnExistingNull(t *testing.T) {
	doc := map[string]any{"permissions": map[string]any{"defaultMode": nil}}
	if got := collectAbsentLeaves(doc, absentDefaults()); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestCollectAbsentLeavesKeepsSiblingsOfAnExistingContainer(t *testing.T) {
	doc := map[string]any{"permissions": map[string]any{"allow": []any{}}}
	got := collectAbsentLeaves(doc, absentDefaults())
	want := []leafRec{{Path: []string{"permissions", "defaultMode"}, Hash: hashLeaf("default")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSetLeavesCreatesParents(t *testing.T) {
	doc := map[string]any{}
	defaults := absentDefaults()
	setLeaves(doc, defaults, collectAbsentLeaves(doc, defaults))
	want := map[string]any{"permissions": map[string]any{"defaultMode": "default"}}
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("got %v, want %v", doc, want)
	}
}
