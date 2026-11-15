package modelapi

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"testing"
)

// TestFixtureMatchesTypes checks that every member of the contract fixture
// docs/contracts/model-api.sample.json decodes strictly into its wire type and
// re-marshals to the same JSON. It matters because the fixture is the one
// document other-language clients and later tasks read: a renamed key, a
// dropped omitempty or an unknown field breaks the wire contract silently.
func TestFixtureMatchesTypes(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/contracts/model-api.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	members := map[string]any{
		"ping":          &Ping{},
		"catalog":       &Catalog{},
		"job":           &JobStatus{},
		"start_request": &StartRequest{},
		"problem":       &Problem{},
	}
	if len(doc) != len(members) {
		t.Fatalf("fixture has %d members, want %d", len(doc), len(members))
	}
	for name, ptr := range members {
		member, ok := doc[name]
		if !ok {
			t.Fatalf("fixture lacks %q", name)
		}
		dec := json.NewDecoder(bytes.NewReader(member))
		dec.DisallowUnknownFields()
		if err := dec.Decode(ptr); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, err := json.Marshal(ptr)
		if err != nil {
			t.Fatal(err)
		}
		var want, got any
		if err := json.Unmarshal(member, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(again, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s does not round-trip:\nfixture: %s\nmarshal: %s", name, member, again)
		}
	}
}

// TestEveryProblemCodeIsDocumented lists every Code* constant and asserts each
// is a unique lowercase snake_case string, and that the fixture's problem uses
// a listed code. It matters because clients switch on these strings, so a
// duplicate or oddly spelled code would make two failures indistinguishable.
func TestEveryProblemCodeIsDocumented(t *testing.T) {
	codes := map[string]string{
		"CodeOccupied":         CodeOccupied,
		"CodeOthers":           CodeOthers,
		"CodeOccupancyUnknown": CodeOccupancyUnknown,
		"CodeStillFinishing":   CodeStillFinishing,
		"CodeInUse":            CodeInUse,
		"CodeStopped":          CodeStopped,
		"CodeFailed":           CodeFailed,
		"CodeCancelled":        CodeCancelled,
		"CodeUnknownModel":     CodeUnknownModel,
		"CodeBlocked":          CodeBlocked,
	}
	snake := regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)
	seen := map[string]string{}
	for name, v := range codes {
		if !snake.MatchString(v) {
			t.Errorf("%s = %q is not lowercase snake_case", name, v)
		}
		if other, dup := seen[v]; dup {
			t.Errorf("%s and %s share the value %q", name, other, v)
		}
		seen[v] = name
	}
	raw, err := os.ReadFile("../../../docs/contracts/model-api.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Problem Problem `json:"problem"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen[doc.Problem.Code]; !ok {
		t.Errorf("fixture problem code %q is not a Code* constant", doc.Problem.Code)
	}
}
