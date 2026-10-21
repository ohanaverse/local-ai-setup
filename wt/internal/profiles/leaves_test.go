// wt/internal/profiles/leaves_test.go
package profiles

import (
	"encoding/json"
	"fmt"
	"math/rand"
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

// TestHashLeafHoldsForNumbersPastFloat64sExactRange verifies a written
// leaf's hash equals the hash of what a read back from the file gives, even
// for integers past float64's exact range (2^53 and up): the profile writes
// an int64, the restored file parses the same JSON number into a float64
// one unit away, and hashing the write-side form alone used to hash the two
// differently — restore then read the profile's own untouched leaf as
// changed by the session and left it un-restored.
func TestHashLeafHoldsForNumbersPastFloat64sExactRange(t *testing.T) {
	const big = int64(9007199254740993) // 2^53 + 1
	b, err := json.Marshal(big)
	if err != nil {
		t.Fatal(err)
	}
	var back any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if hashLeaf(big) != hashLeaf(back) {
		t.Errorf("hashLeaf(int64 %s) != hashLeaf(what the file parses it back to)", b)
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

// deepCopy copies maps and arrays recursively, so the copy shares no map or
// array with v. Scalars are immutable and come back as they are. A copy that
// shared an array would let an in-place write by the code under test change
// the original the test compares against.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, sub := range t {
			out[k] = deepCopy(sub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopy(e)
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

// TestRevertLeavesLegacyRecordRestoresUnconditionally verifies a hashless
// record (an older wt's top-level key) restores the original value even
// though the session changed it: an older marker carries no hash to compare
// with, so it keeps that wt's restore rule.
func TestRevertLeavesLegacyRecordRestoresUnconditionally(t *testing.T) {
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

// TestRevertLeavesKeepsSessionAppendsToAnArrayLeaf: a profile pins
// permissions.allow and the session appends a grant. Restore takes the
// profile's elements out, keeps the grant and puts the original elements
// first; with nothing appended the leaf is restored whole.
func TestRevertLeavesKeepsSessionAppendsToAnArrayLeaf(t *testing.T) {
	orig := map[string]any{"permissions": map[string]any{"allow": []any{"Bash(git:*)"}}}
	content := map[string]any{"permissions": map[string]any{"allow": []any{"Bash(ls)"}}}
	recs := collectLeaves(orig, content)

	cur := map[string]any{"permissions": map[string]any{"allow": []any{"Bash(ls)", "Bash(make:*)"}}}
	revertLeaves(cur, orig, recs)
	got, _ := getPath(cur, []string{"permissions", "allow"})
	if want := []any{"Bash(git:*)", "Bash(make:*)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("appended: allow = %v, want %v", got, want)
	}

	cur = map[string]any{"permissions": map[string]any{"allow": []any{"Bash(ls)"}}}
	revertLeaves(cur, orig, recs)
	got, _ = getPath(cur, []string{"permissions", "allow"})
	if want := []any{"Bash(git:*)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("untouched: allow = %v, want %v", got, want)
	}

	// No original array: only the session's grant stays.
	recs = collectLeaves(map[string]any{}, content)
	cur = map[string]any{"permissions": map[string]any{"allow": []any{"Bash(ls)", "Bash(make:*)"}}}
	revertLeaves(cur, map[string]any{}, recs)
	got, _ = getPath(cur, []string{"permissions", "allow"})
	if want := []any{"Bash(make:*)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("no original: allow = %v, want %v", got, want)
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

// parityKeys is the generator's key alphabet: three names, so a doc and a
// content drawn independently share keys at most levels and the
// type-conflict and empty-object cases come up in most runs.
var parityKeys = []string{"a", "b", "c"}

// parityMaxDepth bounds a generated path's length.
const parityMaxDepth = 3

// genParityMap draws an object at path length depth (0: the root) holding
// 0-3 keys of parityKeys. The keys are visited in alphabet order, never by
// ranging over a map, so one seed always gives the same document.
func genParityMap(rng *rand.Rand, depth int) map[string]any {
	m := map[string]any{}
	for _, k := range parityKeys {
		if rng.Intn(3) == 0 {
			continue
		}
		m[k] = genParityValue(rng, depth+1)
	}
	return m
}

// genParityValue draws a value at path length depth: a string, a number
// (an int or a float64, as TOML and JSON give them), a bool, null, an array
// (empty, of scalars, or holding an object), an empty object, or below
// parityMaxDepth a nested object, which is drawn most often so collisions
// between two objects (the recursive case) are common.
func genParityValue(rng *rand.Rand, depth int) any {
	n := 9
	if depth >= parityMaxDepth {
		n = 7 // no nested object at the deepest level
	}
	switch rng.Intn(n) {
	case 0:
		return []string{"x", "y"}[rng.Intn(2)]
	case 1:
		if rng.Intn(2) == 0 {
			return rng.Intn(3)
		}
		return float64(rng.Intn(3)) + 0.5
	case 2:
		return rng.Intn(2) == 0
	case 3:
		return nil
	case 4:
		switch rng.Intn(3) {
		case 0:
			return []any{}
		case 1:
			return []any{"x", float64(1)}
		default:
			return []any{map[string]any{"a": "x"}}
		}
	case 5, 6:
		return map[string]any{}
	default:
		return genParityMap(rng, depth)
	}
}

// parityLeaves returns the path of every leaf of doc (a value that is not a
// non-empty object); the value is read back through getPath where it is
// needed.
func parityLeaves(doc map[string]any) [][]string {
	var out [][]string
	var walk func(prefix []string, m map[string]any)
	walk = func(prefix []string, m map[string]any) {
		for k, v := range m {
			p := append(append([]string(nil), prefix...), k)
			if sub, ok := v.(map[string]any); ok && len(sub) > 0 {
				walk(p, sub)
				continue
			}
			out = append(out, p)
		}
	}
	walk(nil, doc)
	return out
}

// pathUnder reports whether p equals prefix or lies below it.
func pathUnder(p, prefix []string) bool {
	return len(p) >= len(prefix) && reflect.DeepEqual(p[:len(prefix)], prefix)
}

// sameMap reports whether a and b are the very same map object.
func sameMap(a, b any) bool {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	return aok && bok && reflect.ValueOf(am).UnsafePointer() == reflect.ValueOf(bm).UnsafePointer()
}

// TestCollectLeavesMatchesMergeInto verifies collectLeaves records exactly
// the leaves mergeInto writes, over a few hundred generated (doc, content)
// pairs. They are two walks over the same pair (#411); restore trusts the
// records to name what the merge wrote, so a change to one walk alone would
// make cleanup leave a profile's value behind in the user's settings file
// (a leaf written but not recorded) or delete or overwrite the user's own
// value (a leaf recorded but not written). Three properties are checked
// against the real merge, never against a second copy of the rule:
//
//  1. each record's path exists in the merged document and its value hashes
//     to the record's Hash and Elems (the record describes what was written);
//  2. completeness: every leaf path that differs between doc and the merged
//     document, in either direction, is a recorded path or lies under one;
//  3. soundness: every record names a leaf of content (not a non-empty
//     object), and where that leaf is an object the merged document holds
//     that very object — so {} merged into an existing object, which
//     mergeInto leaves alone, cannot be recorded.
//
// The generator is seeded with a fixed value and walks keys in a fixed
// order, so every run checks the same cases and a failure is the same
// failure on every machine. The message names the case number and prints
// doc, content and the offending path; rerun with
// `go test ./internal/profiles -run TestCollectLeavesMatchesMergeInto -v`
// and paste the printed doc and content into a table test to pin it.
func TestCollectLeavesMatchesMergeInto(t *testing.T) {
	const seed, cases = 411, 500
	rng := rand.New(rand.NewSource(seed))
	var typeConflicts, emptyOverMissing, emptyIntoObject, scalarOverObject int
	reported := 0
	for i := 0; i < cases; i++ {
		if reported > 0 {
			break // one failing case is the counterexample; the rest is noise
		}
		doc := genParityMap(rng, 0)
		content := genParityMap(rng, 0)
		// Snapshot both inputs before either walk runs: everything below is
		// judged against what the generator drew, so a walk that wrote into
		// its own input would otherwise move the baseline with it and hide the
		// change.
		docBefore := deepCopy(doc).(map[string]any)
		contentBefore := deepCopy(content).(map[string]any)
		fail := func(path []string, format string, args ...any) {
			t.Helper()
			reported++
			t.Errorf("case %d (seed %d): doc %s, content %s, path %q: %s",
				i, seed, mustJSON(docBefore), mustJSON(contentBefore), path, fmt.Sprintf(format, args...))
		}

		recs := collectLeaves(doc, content)
		if !reflect.DeepEqual(doc, docBefore) {
			fail(nil, "collectLeaves changed the document it was given")
		}
		if !reflect.DeepEqual(content, contentBefore) {
			fail(nil, "collectLeaves changed the content it was given")
		}
		merged := deepCopy(docBefore).(map[string]any)
		src := deepCopy(contentBefore).(map[string]any)
		mergeInto(merged, src)

		seen := map[string]bool{}
		for _, r := range recs {
			key := strings.Join(r.Path, "\x00")
			if seen[key] {
				fail(r.Path, "recorded twice")
			}
			seen[key] = true

			// (1) The record describes what the merge wrote.
			got, ok := getPath(merged, r.Path)
			if !ok {
				fail(r.Path, "recorded but absent from the merged document %s", mustJSON(merged))
				continue
			}
			if h := hashLeaf(got); h != r.Hash {
				fail(r.Path, "recorded hash does not match the merged value %s", mustJSON(got))
			}
			if e := elemHashes(got); !reflect.DeepEqual(e, r.Elems) {
				fail(r.Path, "recorded element hashes %v, merged value's are %v", r.Elems, e)
			}

			// (3) The record is a write the merge performed.
			cv, ok := getPath(src, r.Path)
			if !ok {
				fail(r.Path, "recorded but content has no value there")
				continue
			}
			if sub, isMap := cv.(map[string]any); isMap {
				if len(sub) > 0 {
					fail(r.Path, "recorded a non-empty object %s as one leaf", mustJSON(cv))
				} else if !sameMap(got, cv) {
					fail(r.Path, "recorded {} but the merge kept the existing object %s", mustJSON(got))
				}
			}
		}

		// (2) Every leaf the merge changed is covered by a record.
		covered := func(p []string) bool {
			for _, r := range recs {
				if pathUnder(p, r.Path) {
					return true
				}
			}
			return false
		}
		for _, p := range parityLeaves(merged) {
			mv, _ := getPath(merged, p)
			dv, inDoc := getPath(docBefore, p)
			if (!inDoc || !reflect.DeepEqual(dv, mv)) && !covered(p) {
				fail(p, "merge wrote %s (doc had %s, present %v) but no record covers it",
					mustJSON(mv), mustJSON(dv), inDoc)
			}
		}
		for _, p := range parityLeaves(docBefore) {
			dv, _ := getPath(docBefore, p)
			mv, inMerged := getPath(merged, p)
			if sub, isMap := mv.(map[string]any); inMerged && isMap && len(sub) > 0 {
				continue // a leaf the merge grew into an object: its leaves are checked above
			}
			if (!inMerged || !reflect.DeepEqual(dv, mv)) && !covered(p) {
				fail(p, "doc's %s became %s (present %v) but no record covers it",
					mustJSON(dv), mustJSON(mv), inMerged)
			}
		}

		// Tally the cases the walks can drift on, so a generator that stops
		// producing them fails below instead of passing vacuously.
		var tally func(d, c map[string]any)
		tally = func(d, c map[string]any) {
			for k, cv := range c {
				dv, found := d[k]
				dm, dIsMap := dv.(map[string]any)
				cm, cIsMap := cv.(map[string]any)
				switch {
				case cIsMap && len(cm) == 0 && !found:
					emptyOverMissing++
				case cIsMap && len(cm) == 0 && dIsMap:
					emptyIntoObject++
				case cIsMap && found && !dIsMap:
					typeConflicts++
				case !cIsMap && dIsMap:
					scalarOverObject++
				case cIsMap && dIsMap:
					tally(dm, cm)
				}
			}
		}
		tally(docBefore, contentBefore)
	}
	if reported > 0 {
		return // the run stopped at its first failing case, so the tallies are partial
	}
	for name, n := range map[string]int{
		"{} over a missing key":       emptyOverMissing,
		"{} into an existing object":  emptyIntoObject,
		"an object over a non-object": typeConflicts,
		"a non-object over an object": scalarOverObject,
	} {
		if n < 20 {
			t.Errorf("generator produced %q only %d times in %d cases; the property is not being exercised", name, n, cases)
		}
	}
}

// mustJSON renders v for a failure message, falling back to %#v if it cannot
// be marshaled, so reporting a failure never fails itself.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}
