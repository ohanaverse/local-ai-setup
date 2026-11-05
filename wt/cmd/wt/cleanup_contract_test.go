package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

const (
	cleanupFixture = "../../../docs/contracts/cleanup-list.sample.json"
	cleanupSchema  = "../../../docs/contracts/cleanup-list.schema.json"
	cleanupPkgDir  = "../../internal/cleanup"
)

func readJSONFile(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// schemaKeywords are the only JSON Schema keywords the validator below
// models; a schema using any other keyword fails the test rather than being
// silently unchecked.
var schemaKeywords = []string{"$schema", "$id", "title", "description", "$defs", "$ref",
	"type", "required", "enum", "const", "properties", "additionalProperties", "items"}

// validateSchema is a purpose-built validator (no JSON Schema module is in
// go.mod) for type, required, enum, const, properties, additionalProperties
// (false only), items, plus local "#/$defs/<name>" references.
func validateSchema(root, schema map[string]any, v any, path string) []string {
	var errs []string
	for k := range schema {
		if !slices.Contains(schemaKeywords, k) {
			errs = append(errs, fmt.Sprintf("%s: schema uses unmodelled keyword %q", path, k))
		}
	}
	if ref, ok := schema["$ref"].(string); ok {
		name, found := strings.CutPrefix(ref, "#/$defs/")
		defs, _ := root["$defs"].(map[string]any)
		target, ok := defs[name].(map[string]any)
		if !found || !ok {
			return append(errs, fmt.Sprintf("%s: unresolvable $ref %q", path, ref))
		}
		return append(errs, validateSchema(root, target, v, path)...)
	}
	if typ, ok := schema["type"].(string); ok && jsonType(v) != typ {
		return append(errs, fmt.Sprintf("%s: type %s, want %s", path, jsonType(v), typ))
	}
	if c, ok := schema["const"]; ok && !reflect.DeepEqual(c, v) {
		errs = append(errs, fmt.Sprintf("%s: %v, want const %v", path, v, c))
	}
	if enum, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		errs = append(errs, fmt.Sprintf("%s: %v not in enum %v", path, v, enum))
	}
	switch val := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, has := val[r.(string)]; !has {
					errs = append(errs, fmt.Sprintf("%s: missing required %q", path, r))
				}
			}
		}
		for k, child := range val {
			ps, known := props[k].(map[string]any)
			if !known {
				if ap, ok := schema["additionalProperties"].(bool); ok && !ap {
					errs = append(errs, fmt.Sprintf("%s: key %q not allowed", path, k))
				}
				continue
			}
			errs = append(errs, validateSchema(root, ps, child, path+"."+k)...)
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, el := range val {
				errs = append(errs, validateSchema(root, items, el, path+"["+strconv.Itoa(i)+"]")...)
			}
		}
	}
	return errs
}

func jsonType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case float64:
		// encoding/json cannot tell 1 from 1.0; the contract uses integers.
		if f := v.(float64); f == float64(int64(f)) {
			return "integer"
		}
		return "number"
	}
	return "unknown"
}

// TestCleanupFixtureMatchesEnvelopeShape pins docs/contracts/cleanup-list.sample.json
// to cleanup.Envelope in both directions (every key the fixture carries is one
// wt writes, with that JSON type, and every key wt always writes is present),
// validates the fixture and live Encode/EncodeError output against the
// published schema (enums included), and requires the fixture to show one row
// of each status and kind, an unknown row, a blocker row and an evidence-less
// row. Why: a script in another language reads this document; a renamed Go
// json tag must fail here until the schema and fixture move with it.
func TestCleanupFixtureMatchesEnvelopeShape(t *testing.T) {
	raw, err := os.ReadFile(cleanupFixture)
	if err != nil {
		t.Fatal(err)
	}
	checkJSONShape(t, "document", raw, reflect.TypeFor[cleanup.Envelope]())

	var doc struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range doc.Rows {
		seen["status:"+r["status"].(string)] = true
		seen["kind:"+r["kind"].(string)] = true
		if r["unknown"] == true {
			seen["unknown"] = true
		}
		if b, _ := r["blockers"].([]any); len(b) > 0 {
			seen["blockers"] = true
		}
		if _, has := r["evidence"]; !has {
			seen["no-evidence"] = true
		}
	}
	for _, want := range []string{"status:proven", "status:less_sure", "status:unique_work", "status:in_use",
		"kind:worktree", "kind:branch", "kind:remote", "unknown", "blockers", "no-evidence"} {
		if !seen[want] {
			t.Errorf("the fixture has no row for %q", want)
		}
	}
	// Each row key the type may omit must appear somewhere, so omitempty
	// keys are pinned too.
	rowKeys := map[string]bool{}
	for _, r := range doc.Rows {
		for k := range r {
			rowKeys[k] = true
		}
	}
	for k := range jsonFieldSpecs(t, reflect.TypeFor[cleanup.RowJSON]()) {
		if !rowKeys[k] {
			t.Errorf("no fixture row carries %q", k)
		}
	}

	schema := readJSONFile(t, cleanupSchema).(map[string]any)
	if errs := validateSchema(schema, schema, readJSONFile(t, cleanupFixture), "fixture"); len(errs) > 0 {
		t.Errorf("fixture violates the schema:\n%s", strings.Join(errs, "\n"))
	}
	// What wt prints must pass too, success and failure.
	okDoc, err := cleanup.Encode(cleanup.Report{Repo: "/r", GH: cleanup.GHOK,
		Rows: []cleanup.Row{{Kind: cleanup.KindBranch, Name: "x", Status: cleanup.StatusLessSure, Unknown: true}}})
	if err != nil {
		t.Fatal(err)
	}
	errDoc, err := cleanup.EncodeError("/r", errors.New("boom"))
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"Encode": okDoc, "EncodeError": errDoc} {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		if errs := validateSchema(schema, schema, v, name); len(errs) > 0 {
			t.Errorf("%s output violates the schema:\n%s", name, strings.Join(errs, "\n"))
		}
	}
	// A bad value must be caught: the validator itself is under test.
	bad := map[string]any{"version": 1.0, "repo": "/r", "rows": []any{map[string]any{"status": "proved"}}}
	if errs := validateSchema(schema, schema, bad, "bad"); len(errs) == 0 {
		t.Error("validator accepted a row with a misspelt status")
	}
}

// TestCleanupSchemaEnumsCoverEveryCode pins the schema's enums to the
// constants in internal/cleanup (read from source, so a constant added later
// is seen): every status, kind, evidence and gh value, every Reason* code
// (reasons) and every Block* code (blockers) must be listed, and nothing
// else. Why: a new reason code reaching users without a schema and fixture
// update would otherwise pass every test.
func TestCleanupSchemaEnumsCoverEveryCode(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, cleanupPkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, s := range gd.Specs {
					vs := s.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						val, _ := strconv.Unquote(lit.Value)
						typ := ""
						if id, ok := vs.Type.(*ast.Ident); ok {
							typ = id.Name
						}
						switch {
						case typ == "Evidence":
							got["evidence"] = append(got["evidence"], val)
						case typ == "Status":
							got["status"] = append(got["status"], val)
						case typ == "Kind":
							got["kind"] = append(got["kind"], val)
						case typ == "GHState":
							got["gh"] = append(got["gh"], val)
						case typ == "" && strings.HasPrefix(n.Name, "Reason"):
							got["reasons"] = append(got["reasons"], val)
						case typ == "" && strings.HasPrefix(n.Name, "Block"):
							got["blockers"] = append(got["blockers"], val)
						}
					}
				}
			}
		}
	}
	schema := readJSONFile(t, cleanupSchema).(map[string]any)
	defs := schema["$defs"].(map[string]any)
	enumOf := func(def string, path ...string) []string {
		t.Helper()
		cur := defs[def].(map[string]any)
		for _, p := range path {
			cur = cur[p].(map[string]any)
			if ref, ok := cur["$ref"].(string); ok {
				cur = defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			}
		}
		var out []string
		for _, e := range cur["enum"].([]any) {
			out = append(out, e.(string))
		}
		return out
	}
	want := map[string][]string{
		"status":   enumOf("row", "properties", "status"),
		"kind":     enumOf("row", "properties", "kind"),
		"evidence": enumOf("row", "properties", "evidence"),
		"reasons":  enumOf("row", "properties", "reasons", "items", "properties", "code"),
		"blockers": enumOf("row", "properties", "blockers", "items", "properties", "code"),
		"gh":       schemaEnum2(schema),
	}
	for name, w := range want {
		g := slices.Clone(got[name])
		w = slices.Clone(w)
		slices.Sort(g)
		slices.Sort(w)
		if len(g) == 0 || !slices.Equal(g, w) {
			t.Errorf("%s: source constants %v, schema enum %v", name, g, w)
		}
	}
}

func schemaEnum2(schema map[string]any) []string {
	var out []string
	for _, e := range schema["properties"].(map[string]any)["gh"].(map[string]any)["enum"].([]any) {
		out = append(out, e.(string))
	}
	return out
}
