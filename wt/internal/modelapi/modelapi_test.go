package modelapi

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strings"
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
		"ping":             &Ping{},
		"catalog":          &Catalog{},
		"job":              &JobStatus{},
		"start_request":    &StartRequest{},
		"stop_request":     &StopRequest{},
		"session_register": &SessionRegister{},
		"session_beat":     &SessionBeat{},
		"problem":          &Problem{},
		"problem_in_use":   &Problem{},
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
	// The table must list every Code* constant the package declares.
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "modelapi.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			for _, n := range spec.(*ast.ValueSpec).Names {
				if _, ok := codes[n.Name]; !ok && strings.HasPrefix(n.Name, "Code") {
					t.Errorf("constant %s is missing from the codes table in this test", n.Name)
				}
			}
		}
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
		InUse   Problem `json:"problem_in_use"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Problem{doc.Problem, doc.InUse} {
		if _, ok := seen[p.Code]; !ok {
			t.Errorf("fixture problem code %q is not a Code* constant", p.Code)
		}
	}
	if doc.InUse.Code != CodeInUse || len(doc.InUse.Sessions) == 0 {
		t.Errorf("fixture problem_in_use = %+v, want code in_use with sessions", doc.InUse)
	}
}
