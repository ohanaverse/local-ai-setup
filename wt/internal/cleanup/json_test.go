package cleanup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func decodeDoc(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, raw)
	}
	return doc
}

// TestEnvelopeVersioned pins that every document carries `version` equal to
// EnvelopeVersion (1), and that `rows` is present even for an empty report.
// A consumer branches on the version and iterates rows without a nil check,
// so neither may ever go missing. Output is also deterministic and ends in a
// newline.
func TestEnvelopeVersioned(t *testing.T) {
	raw, err := Encode(Report{Repo: "/r", GH: GHOK})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, raw)
	if string(doc["version"]) != "1" || EnvelopeVersion != 1 {
		t.Errorf("version = %s, EnvelopeVersion = %d; want 1", doc["version"], EnvelopeVersion)
	}
	if string(doc["rows"]) != "[]" {
		t.Errorf("rows = %s; want [] for an empty report", doc["rows"])
	}
	if !bytes.HasSuffix(raw, []byte("}\n")) {
		t.Errorf("output must end with a newline: %q", raw[max(0, len(raw)-5):])
	}
	again, _ := Encode(Report{Repo: "/r", GH: GHOK})
	if !bytes.Equal(raw, again) {
		t.Error("Encode is not deterministic")
	}
}

// TestEnvelopeAbsentVersusUnknown pins the contract's central distinction:
// a less_sure row whose merge proof failed has `unknown: true` and NO
// `evidence` key (never null, never "unknown"), while a proven row has an
// evidence string and `unknown: false`. Consumers read absence as "no proof
// found" and the boolean as "could not be determined".
func TestEnvelopeAbsentVersusUnknown(t *testing.T) {
	rep := Report{
		Repo: "/r", GH: GHOK,
		Base: Base{Default: "main", Ref: "origin/main", FetchedAt: time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)},
		Rows: []Row{
			{Kind: KindBranch, Name: "a", Branch: "a", Status: StatusLessSure, Unknown: true,
				Reasons: []Reason{{ReasonMergeProofUnavailable, mergeProofDetail}}},
			{Kind: KindBranch, Name: "b", Branch: "b", Status: StatusProven, Evidence: EvAncestor},
		},
	}
	raw, err := Encode(rep)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, has := doc.Rows[0]["evidence"]; has {
		t.Errorf("row with no proof must omit evidence: %s", raw)
	}
	if string(doc.Rows[0]["unknown"]) != "true" {
		t.Errorf("unknown = %s; want true", doc.Rows[0]["unknown"])
	}
	if string(doc.Rows[1]["evidence"]) != `"ancestor"` || string(doc.Rows[1]["unknown"]) != "false" {
		t.Errorf("proven row: evidence=%s unknown=%s", doc.Rows[1]["evidence"], doc.Rows[1]["unknown"])
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("no key may be null: %s", raw)
	}
	for _, r := range doc.Rows {
		for _, k := range []string{"reasons", "blockers"} {
			if !strings.HasPrefix(string(r[k]), "[") {
				t.Errorf("%s must always be an array, got %s", k, r[k])
			}
		}
	}
}

// TestEnvelopeErrorIsOneDocument pins that a failure still prints one
// envelope: version, repo, `error`, an empty `rows` array, and none of the
// result keys (base, fetch, gh). A script piping stdout into a JSON parser
// must never see anything but that single document.
func TestEnvelopeErrorIsOneDocument(t *testing.T) {
	raw, err := EncodeError("/r", errors.New("boom"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var env Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatal(err)
	}
	if dec.More() {
		t.Error("more than one document")
	}
	if env.Version != EnvelopeVersion || env.Repo != "/r" || env.Error == nil || env.Error.Message != "boom" {
		t.Errorf("envelope = %+v", env)
	}
	doc := decodeDoc(t, raw)
	if string(doc["rows"]) != "[]" {
		t.Errorf("rows = %s; want []", doc["rows"])
	}
	for _, k := range []string{"base", "fetch", "gh"} {
		if _, has := doc[k]; has {
			t.Errorf("error document carries %q", k)
		}
	}
}

// TestStatusAndEvidenceEnumsStable pins the exact wire strings of the
// status, kind, evidence and gh enums. They are a published contract; a
// rename here must be a deliberate version bump, not a refactor side effect.
func TestStatusAndEvidenceEnumsStable(t *testing.T) {
	check := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v; want %v", name, got, want)
		}
	}
	check("status", []string{string(StatusProven), string(StatusLessSure), string(StatusUniqueWork), string(StatusInUse)},
		[]string{"proven", "less_sure", "unique_work", "in_use"})
	check("kind", []string{string(KindWorktree), string(KindBranch), string(KindRemote)},
		[]string{"worktree", "branch", "remote"})
	check("gh", []string{string(GHOK), string(GHUnavailable), string(GHPartial)},
		[]string{"ok", "unavailable", "partial"})
	check("evidence", []string{string(EvSameCommit), string(EvAncestor), string(EvTreesMatch), string(EvNoAddedChanges),
		string(EvMergeAddsNothing), string(EvPRTip), string(EvCherryEquivalent), string(EvPatchID)},
		[]string{"same-commit", "ancestor", "trees-match", "no-added-changes", "merge-adds-nothing", "pr-tip", "cherry-equivalent", "patch-id"})
}

// TestEncodeWeakOnlyRowHasNoEvidence classifies a real branch whose only
// signal is weak (a squash that was then reverted, so cherry/patch-id
// match) and encodes it. Why: Row.Evidence only ever holds a proof, so the
// JSON row must carry no `evidence` key and a `weak_signal` reason; a
// weak signal shown as evidence would read as a proof to a consumer.
func TestEncodeWeakOnlyRowHasNoEvidence(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Checkout("topic")
	f.Commit("a", "a", "a")
	f.Push("topic")
	f.Squash("topic")
	f.Revert("HEAD")
	f.Push("main")
	noGh(t)
	r := Classify(gather(t, f), Options{Now: later(), MinAge: DefaultMinAge})
	raw, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var row map[string]json.RawMessage
	for _, c := range doc.Rows {
		if string(c["name"]) == `"topic"` {
			row = c
		}
	}
	if row == nil {
		t.Fatalf("no topic row: %s", raw)
	}
	if _, has := row["evidence"]; has {
		t.Errorf("weak-only row carries evidence: %s", row["evidence"])
	}
	if !strings.Contains(string(row["reasons"]), `"weak_signal"`) {
		t.Errorf("reasons = %s; want a weak_signal", row["reasons"])
	}
}

// TestEncodeFetchAbsentUnlessAttempted pins `fetch` as "absent = no fetch
// ran": a report whose Fetch is zero has no `fetch` key, and a failed fetch
// reads {"ok":false,"error":...}. Why: {"ok":false} for a run that never
// fetched would tell a consumer the base may be stale when nothing was tried.
func TestEncodeFetchAbsentUnlessAttempted(t *testing.T) {
	raw, _ := Encode(Report{Repo: "/r", Base: Base{Default: "main", Ref: "origin/main"}})
	if _, has := decodeDoc(t, raw)["fetch"]; has {
		t.Errorf("no fetch ran, yet fetch is present: %s", raw)
	}
	at := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	raw, _ = Encode(Report{Repo: "/r", Fetch: FetchResult{Err: "no network", At: at}})
	var doc struct {
		Fetch map[string]json.RawMessage `json:"fetch"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc.Fetch["ok"]) != "false" || string(doc.Fetch["error"]) != `"no network"` {
		t.Errorf("failed fetch = %v", doc.Fetch)
	}
}

// TestEncodeFetchedAtOnlyAfterAGoodFetch gathers with a fetch that works,
// one that fails and none at all, and encodes each. Why: `base.fetched_at`
// tells a consumer "the base is current as of then"; after a failed fetch,
// or with none, that is false, so the key must be absent (the failure is in
// `fetch`), and present only when the fetch succeeded.
func TestEncodeFetchedAtOnlyAfterAGoodFetch(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		fetch bool
		want  bool // fetched_at present
	}{
		{"fetch ok", func(*fixture) {}, true, true},
		{"fetch failed", func(f *fixture) {
			f.git("remote", "set-url", "origin", f.Origin+"-missing")
		}, true, false},
		{"no fetch attempted", func(*fixture) {}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			noGh(t)
			s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Fetch: c.fetch})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := Encode(Classify(s, Options{Now: later()}))
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Base map[string]json.RawMessage `json:"base"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if _, has := doc.Base["fetched_at"]; has != c.want {
				t.Fatalf("fetched_at present = %v, want %v (fetch %+v):\n%s", has, c.want, s.Fetch, raw)
			}
		})
	}
	// A report that says the fetch failed never carries fetched_at, even if
	// a caller filled Base.FetchedAt.
	at := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	raw, _ := Encode(Report{Repo: "/r", Base: Base{Default: "main", Ref: "origin/main", FetchedAt: at},
		Fetch: FetchResult{Err: "no network", At: at}})
	if strings.Contains(string(raw), "fetched_at") {
		t.Fatalf("failed fetch carries fetched_at:\n%s", raw)
	}
}

// TestEncodeAgeAbsentWhenUnknown pins that age_days is left out when the
// age could not be determined and written (as 0) for a known age under a
// day. Why: 0 for both would let a consumer read "unknown" as "brand new".
func TestEncodeAgeAbsentWhenUnknown(t *testing.T) {
	raw, _ := Encode(Report{Repo: "/r", Rows: []Row{
		{Kind: KindRemote, Name: "u", Status: StatusUniqueWork},
		{Kind: KindBranch, Name: "k", Status: StatusUniqueWork, AgeKnown: true},
	}})
	var doc struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, has := doc.Rows[0]["age_days"]; has {
		t.Errorf("unknown age written: %s", doc.Rows[0]["age_days"])
	}
	if string(doc.Rows[1]["age_days"]) != "0" {
		t.Errorf("known age under a day = %s; want 0", doc.Rows[1]["age_days"])
	}
}

// TestClassifySetsAgeKnown pins where the classifier marks an age known: a
// local branch with a clock is known; with a zero Now it is not. Why: the
// JSON contract depends on this flag to tell "under a day" from "unknown".
func TestClassifySetsAgeKnown(t *testing.T) {
	f := newFixture(t)
	ancestorTopic(f)
	noGh(t)
	s := gather(t, f)
	if row := findRow(t, Classify(s, Options{Now: later(), MinAge: DefaultMinAge}), KindBranch, "topic"); !row.AgeKnown {
		t.Error("local branch with a clock: AgeKnown = false")
	}
	if row := findRow(t, Classify(s, Options{MinAge: DefaultMinAge}), KindBranch, "topic"); row.AgeKnown {
		t.Error("zero Now: AgeKnown = true")
	}
}

// TestEncodeBaseAbsentWithoutDefault pins that `base` is left out when no
// default branch was found, rather than printing empty strings. Why: "" for
// default and ref would look like a real base.
func TestEncodeBaseAbsentWithoutDefault(t *testing.T) {
	raw, _ := Encode(Report{Repo: "/r"})
	if _, has := decodeDoc(t, raw)["base"]; has {
		t.Errorf("base present without a default branch: %s", raw)
	}
	raw, _ = Encode(Report{Repo: "/r", Base: Base{Default: "main", Ref: "main", Fallback: true}})
	if _, has := decodeDoc(t, raw)["base"]; !has {
		t.Error("base missing with a default branch")
	}
}

// TestEncodeErrorNilError pins that EncodeError(repo, nil) neither panics
// nor emits an empty message: it writes the fixed text "unknown error". Why:
// a caller's bug must not turn the single-document guarantee into a panic.
func TestEncodeErrorNilError(t *testing.T) {
	raw, err := EncodeError("/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil || env.Error.Message != "unknown error" {
		t.Errorf("error = %+v", env.Error)
	}
}

// TestEncodeKeepsEveryNestedPath pins that collapsing the nested-worktree
// blockers into a count is wording only: the JSON still carries one blocker
// per nested worktree, with its path, for a tool that wants them.
func TestEncodeKeepsEveryNestedPath(t *testing.T) {
	row := nestedPrimary(30)
	raw, err := Encode(Report{Repo: "/r", GH: GHOK, Rows: []Row{row}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		p := fmt.Sprintf("/r/.claude/worktrees/agent-%02d", i)
		if !strings.Contains(string(raw), `"`+p+`"`) {
			t.Errorf("JSON lacks nested path %s", p)
		}
	}
	if n := strings.Count(string(raw), `"`+BlockNested+`"`); n != 30 {
		t.Errorf("JSON has %d nested blockers, want 30", n)
	}
}

// TestCleanupJSONUnchangedByNewRowFields encodes rows with Ref, Tip, Head,
// Losses and BranchProven set and compares the document with the same rows
// without them, then checks every row key is one of PR A's. Why: those fields
// serve removal only; the cleanup JSON is a pinned contract
// (docs/contracts/cleanup-list.sample.json), and a new or changed key would
// break every consumer reading it.
func TestCleanupJSONUnchangedByNewRowFields(t *testing.T) {
	tip := strings.Repeat("c", 40)
	bare := []Row{
		{Kind: KindWorktree, Name: "topic", Path: "/w/topic", Branch: "topic", Status: StatusUniqueWork,
			Evidence: EvMergeAddsNothing, Reasons: []Reason{{ReasonDirty, "uncommitted or untracked files"}}},
		{Kind: KindBranch, Name: "solo", Branch: "solo", Status: StatusUniqueWork, Reasons: []Reason{{ReasonUnpushed, "3 commits on no remote-tracking ref"}}},
	}
	full := slices.Clone(bare)
	full[0].Ref, full[0].Tip, full[0].Head, full[0].BranchProven = "refs/heads/topic", tip, tip, true
	full[0].Losses = Losses{Dirty: true, IgnoredEntries: 2, IgnoredBytes: 10, DetachedUnique: true, GatesUnknown: true}
	full[1].Ref, full[1].Tip = "refs/heads/solo", tip
	full[1].Losses = Losses{Unpushed: 3}
	rep := func(rows []Row) Report {
		return Report{Repo: "/r", GH: GHOK, Base: Base{Default: "main", Ref: "origin/main"}, Rows: rows}
	}
	want, err := Encode(rep(bare))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Encode(rep(full))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("new row fields changed the JSON:\n got %s\nwant %s", got, want)
	}
	var doc struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	prA := []string{"kind", "name", "path", "branch", "remote", "status", "evidence", "unknown", "age_days", "reasons", "blockers", "pr"}
	for _, row := range doc.Rows {
		for k := range row {
			if !slices.Contains(prA, k) {
				t.Errorf("row carries key %q, not in PR A's row shape", k)
			}
		}
	}
}

// TestEnvelopeCarriesNotes pins that the caveats the text header prints (a
// lowered min-age and the caller's Notes) are in the JSON `notes` array, and
// that the key is absent when there are none. Why: a script reading rows
// cannot otherwise learn that artifact protection is off.
func TestEnvelopeCarriesNotes(t *testing.T) {
	raw, err := Encode(Report{Repo: "/r", GH: GHOK, MinAge: time.Minute,
		Notes: []string{"registry: failed to load; artifact protection is OFF"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Notes []string }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Notes) != 2 || !strings.HasPrefix(doc.Notes[0], "min-age: lowered to 1m") || !strings.Contains(doc.Notes[1], "protection is OFF") {
		t.Errorf("notes = %q", doc.Notes)
	}
	plain, _ := Encode(Report{Repo: "/r", GH: GHOK})
	if _, has := decodeDoc(t, plain)["notes"]; has {
		t.Error("notes present for a report with none")
	}
}

// TestEncodeFetchRemotes encodes a report fetched from two remotes, one of
// which failed. What: fetch.remotes lists both with their own result, while
// fetch.ok stays origin's; a single-remote repository omits the key. Why: the
// header and JSON must say exactly which remotes were fetched, and the key is
// additive so older consumers are unaffected.
func TestEncodeFetchRemotes(t *testing.T) {
	two := FetchResult{OK: true, At: time.Now(), Remotes: []RemoteFetch{{Name: "origin", OK: true}, {Name: "up", Err: "boom"}}}
	raw, err := Encode(Report{Repo: "/r", Base: Base{Default: "main", Ref: "origin/main"}, Fetch: two})
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	want := []FetchRemoteJSON{{Name: "origin", OK: true}, {Name: "up", Error: "boom"}}
	if env.Fetch == nil || !env.Fetch.OK || !slices.Equal(env.Fetch.Remotes, want) {
		t.Fatalf("remotes missing or wrong:\n%s", raw)
	}
	one := FetchResult{OK: true, At: time.Now(), Remotes: []RemoteFetch{{Name: "origin", OK: true}}}
	raw, _ = Encode(Report{Repo: "/r", Fetch: one})
	if strings.Contains(string(raw), `"remotes"`) {
		t.Fatalf("single remote should omit remotes:\n%s", raw)
	}
}

// TestHeaderNamesOtherRemotes renders the header for a fetch that covered a
// second remote that failed. What: the header names it with its error. Why:
// "fetch: ok" alone would hide that a remote's tracking refs are stale.
func TestHeaderNamesOtherRemotes(t *testing.T) {
	r := Report{Repo: "/r", Fetch: FetchResult{OK: true, At: time.Now(), Remotes: []RemoteFetch{{Name: "origin", OK: true}, {Name: "up", Err: "boom"}, {Name: "fk", Skipped: true}}}}
	h := strings.Join(HeaderLines(r, time.Now()), "\n")
	if !strings.Contains(h, "up FAILED (boom)") || !strings.Contains(h, "skipFetchAll) fk") {
		t.Fatalf("header:\n%s", h)
	}
}
