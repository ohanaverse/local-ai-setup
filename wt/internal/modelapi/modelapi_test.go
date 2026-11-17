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
	"slices"
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
		"ping":                       &Ping{},
		"catalog":                    &Catalog{},
		"job":                        &JobStatus{},
		"jobs":                       &JobList{},
		"start_request":              &StartRequest{},
		"stop_request":               &StopRequest{},
		"session_register":           &SessionRegister{},
		"session_beat":               &SessionBeat{},
		"problem":                    &Problem{},
		"problem_in_use":             &Problem{},
		"candidates":                 &CandidateList{},
		"spend":                      &SpendReport{},
		"error_spend_not_configured": &APIError{},
		"error_spend_unavailable":    &APIError{},

		"error_unauthorized":              &APIError{},
		"error_unknown_model":             &APIError{},
		"error_not_found":                 &APIError{},
		"error_method_not_allowed":        &APIError{},
		"gateway_error_unauthorized":      &GatewayError{},
		"gateway_error_proxy_unavailable": &GatewayError{},
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
		"CodeOccupied":           CodeOccupied,
		"CodeOthers":             CodeOthers,
		"CodeOccupancyUnknown":   CodeOccupancyUnknown,
		"CodeStillFinishing":     CodeStillFinishing,
		"CodeInUse":              CodeInUse,
		"CodeStopped":            CodeStopped,
		"CodeFailed":             CodeFailed,
		"CodeCancelled":          CodeCancelled,
		"CodeUnknownModel":       CodeUnknownModel,
		"CodeBlocked":            CodeBlocked,
		"CodeBadRequest":         CodeBadRequest,
		"CodeUnauthorized":       CodeUnauthorized,
		"CodeNotFound":           CodeNotFound,
		"CodeMethodNotAllowed":   CodeMethodNotAllowed,
		"CodeUnknownJob":         CodeUnknownJob,
		"CodeUnknownSession":     CodeUnknownSession,
		"CodeProxyUnavailable":   CodeProxyUnavailable,
		"CodeSpendNotConfigured": CodeSpendNotConfigured,
		"CodeSpendUnavailable":   CodeSpendUnavailable,
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
	if len(codes) != len(ErrorCodes) {
		t.Errorf("ErrorCodes lists %d codes, the Code* constants are %d", len(ErrorCodes), len(codes))
	}
	for name, v := range codes {
		if !slices.Contains(ErrorCodes, v) {
			t.Errorf("%s is missing from ErrorCodes", name)
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

// TestErrorSamplesUseConstants pins that the fixture's error samples carry the
// shared wording, the OpenAI-style types and codes from ErrorCodes.
func TestErrorSamplesUseConstants(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/contracts/model-api.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Unauthorized  APIError     `json:"error_unauthorized"`
		UnknownModel  APIError     `json:"error_unknown_model"`
		NotFound      APIError     `json:"error_not_found"`
		NotAllowed    APIError     `json:"error_method_not_allowed"`
		GwUnauth      GatewayError `json:"gateway_error_unauthorized"`
		GwUnavailable GatewayError `json:"gateway_error_proxy_unavailable"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Unauthorized.Error != (ErrorObject{Message: MsgUnauthorized, Code: CodeUnauthorized}) {
		t.Errorf("error_unauthorized = %+v", doc.Unauthorized)
	}
	if doc.UnknownModel.Error.Code != CodeUnknownModel || doc.UnknownModel.Error.Message == "" {
		t.Errorf("error_unknown_model = %+v", doc.UnknownModel)
	}
	if doc.NotFound.Error != (ErrorObject{Message: MsgNotFound, Code: CodeNotFound}) {
		t.Errorf("error_not_found = %+v", doc.NotFound)
	}
	if doc.NotAllowed.Error != (ErrorObject{Message: MsgMethodNotAllowed, Code: CodeMethodNotAllowed}) {
		t.Errorf("error_method_not_allowed = %+v", doc.NotAllowed)
	}
	if doc.GwUnauth.Error != (GatewayErrorObject{Message: MsgUnauthorized, Type: TypeAuthentication, Code: CodeUnauthorized}) {
		t.Errorf("gateway_error_unauthorized = %+v", doc.GwUnauth)
	}
	if doc.GwUnavailable.Error != (GatewayErrorObject{Message: MsgProxyUnavailable, Type: TypeAPI, Code: CodeProxyUnavailable}) {
		t.Errorf("gateway_error_proxy_unavailable = %+v", doc.GwUnavailable)
	}
	for _, c := range []string{CodeBadRequest, CodeUnauthorized, CodeNotFound, CodeMethodNotAllowed, CodeUnknownJob, CodeUnknownSession, CodeProxyUnavailable} {
		if !slices.Contains(ErrorCodes, c) {
			t.Errorf("%q missing from ErrorCodes", c)
		}
	}
	var e error = doc.Unauthorized.Err()
	if e.Error() != MsgUnauthorized {
		t.Errorf("Err() = %q", e)
	}
}

func TestStoppableIDs(t *testing.T) {
	got := StoppableIDs([]Resident{{ID: "a", Stoppable: true}, {ID: "b"}, {ID: "c", Stoppable: true}})
	if want := []string{"a", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("StoppableIDs = %v, want %v", got, want)
	}
	if got := StoppableIDs(nil); got != nil {
		t.Errorf("StoppableIDs(nil) = %v, want nil", got)
	}
}
