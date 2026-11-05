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
	"time"

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

// walkSchema checks the WHOLE schema once, whether or not a document reaches
// a part of it: every keyword must be one the validator models, and in the
// form it models (type a string, enum and required arrays, items a single
// schema, additionalProperties a bool). Anything else is an error, so a gap in
// the validator is loud instead of a silently unchecked constraint.
func walkSchema(s map[string]any, path string) []string {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, path+": "+fmt.Sprintf(format, a...)) }
	for k, v := range s {
		if !slices.Contains(schemaKeywords, k) {
			bad("unmodelled keyword %q", k)
			continue
		}
		switch k {
		case "type", "$ref", "$schema", "$id", "title", "description":
			if _, ok := v.(string); !ok {
				bad("%s must be a string (unsupported form %T)", k, v)
			}
		case "enum":
			if _, ok := v.([]any); !ok {
				bad("enum must be an array (unsupported form %T)", v)
			}
		case "required":
			arr, ok := v.([]any)
			if !ok {
				bad("required must be an array (unsupported form %T)", v)
				break
			}
			for _, r := range arr {
				if _, ok := r.(string); !ok {
					bad("required holds a non-string %v", r)
				}
			}
		case "additionalProperties":
			if _, ok := v.(bool); !ok {
				bad("additionalProperties must be a bool (unsupported form %T)", v)
			}
		case "items":
			sub, ok := v.(map[string]any)
			if !ok {
				bad("items must be one schema object (unsupported form %T)", v)
				break
			}
			errs = append(errs, walkSchema(sub, path+".items")...)
		case "properties", "$defs":
			m, ok := v.(map[string]any)
			if !ok {
				bad("%s must be an object", k)
				break
			}
			for name, sub := range m {
				subm, ok := sub.(map[string]any)
				if !ok {
					bad("%s.%s must be a schema object", k, name)
					continue
				}
				errs = append(errs, walkSchema(subm, path+"."+name)...)
			}
		}
	}
	return errs
}

// checkAgainst is the whole check: the schema walk, then validation.
func checkAgainst(schema map[string]any, v any, path string) []string {
	if errs := walkSchema(schema, "schema"); len(errs) > 0 {
		return errs
	}
	return validateSchema(schema, schema, v, path)
}

// validateSchema is a purpose-built validator (no JSON Schema module is in
// go.mod) for type, required, enum, const, properties, additionalProperties
// (false only), items, plus local "#/$defs/<name>" references.
func validateSchema(root, schema map[string]any, v any, path string) []string {
	var errs []string
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
	schema := readJSONFile(t, cleanupSchema).(map[string]any)
	fixtureDoc := readJSONFile(t, cleanupFixture)
	if errs := checkAgainst(schema, fixtureDoc, "fixture"); len(errs) > 0 {
		t.Errorf("fixture violates the schema:\n%s", strings.Join(errs, "\n"))
	}
	// What wt prints must pass too, success, failed fetch and failure.
	okDoc, err := cleanup.Encode(cleanup.Report{Repo: "/r", GH: cleanup.GHOK,
		Rows: []cleanup.Row{{Kind: cleanup.KindBranch, Name: "x", Status: cleanup.StatusLessSure, Unknown: true}}})
	if err != nil {
		t.Fatal(err)
	}
	failedFetch, err := cleanup.Encode(cleanup.Report{Repo: "/r",
		Base:  cleanup.Base{Default: "main", Ref: "origin/main"},
		Fetch: cleanup.FetchResult{Err: "no network", At: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	errDoc, err := cleanup.EncodeError("/r", errors.New("boom"))
	if err != nil {
		t.Fatal(err)
	}
	var errV any
	for name, b := range map[string][]byte{"Encode": okDoc, "EncodeFailedFetch": failedFetch, "EncodeError": errDoc} {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		if name == "EncodeError" {
			errV = v
		}
		if errs := checkAgainst(schema, v, name); len(errs) > 0 {
			t.Errorf("%s output violates the schema:\n%s", name, strings.Join(errs, "\n"))
		}
	}

	// Every key of every struct reachable from Envelope (not just rows) must
	// appear in the fixture or in the live error document, so an omitempty
	// key nobody shows cannot be renamed unnoticed.
	seenKeys := map[reflect.Type]map[string]bool{}
	reachable := map[reflect.Type]bool{}
	walkTyped(t, schema, schema, reflect.TypeFor[cleanup.Envelope](), nil, "envelope",
		func(_ string, typ reflect.Type, _ map[string]any, _ map[string]any) { reachable[typ] = true })
	for _, d := range []any{fixtureDoc, errV} {
		walkTyped(t, schema, schema, reflect.TypeFor[cleanup.Envelope](), d, "envelope",
			func(_ string, typ reflect.Type, _ map[string]any, obj map[string]any) {
				if seenKeys[typ] == nil {
					seenKeys[typ] = map[string]bool{}
				}
				for k := range obj {
					seenKeys[typ][k] = true
				}
			})
	}
	for typ := range reachable {
		for key := range jsonFieldSpecs(t, typ) {
			if !seenKeys[typ][key] {
				t.Errorf("no fixture value (or live error document) carries %s.%s", typ.Name(), key)
			}
		}
	}
}

// derefType strips pointers.
func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// resolveRef follows a local "#/$defs/name" reference.
func resolveRef(root, node map[string]any) map[string]any {
	if ref, ok := node["$ref"].(string); ok {
		return root["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
	}
	return node
}

// walkTyped walks a Go type, its schema node and (optionally) a decoded
// document in step, calling visit for every struct reached: the path, the Go
// type, the schema node and the document's object there (nil when val is nil
// or has no such object). Slices recurse through "items".
func walkTyped(t *testing.T, root, node map[string]any, typ reflect.Type, val any, path string,
	visit func(path string, typ reflect.Type, node map[string]any, obj map[string]any)) {
	t.Helper()
	typ = derefType(typ)
	node = resolveRef(root, node)
	switch typ.Kind() {
	case reflect.Struct:
		obj, _ := val.(map[string]any)
		visit(path, typ, node, obj)
		props, _ := node["properties"].(map[string]any)
		for key, spec := range jsonFieldSpecs(t, typ) {
			if child, ok := props[key].(map[string]any); ok {
				var cv any
				if obj != nil {
					cv = obj[key]
				}
				walkTyped(t, root, child, spec.typ, cv, path+"."+key, visit)
			}
		}
	case reflect.Slice:
		items, ok := node["items"].(map[string]any)
		if !ok {
			return
		}
		arr, _ := val.([]any)
		if len(arr) == 0 {
			walkTyped(t, root, items, typ.Elem(), nil, path+"[]", visit)
		}
		for i, el := range arr {
			walkTyped(t, root, items, typ.Elem(), el, path+"["+strconv.Itoa(i)+"]", visit)
		}
	}
}

// TestCleanupSchemaPropertiesMatchGoTags pins, for every struct reachable
// from cleanup.Envelope, that the schema's `properties` key set equals the Go
// struct's json field set and that `required` is exactly the keys the struct
// always writes (those without omitempty/omitzero). Why: the schema is the
// published contract; without this a tag could be renamed and the schema and
// fixture edited around it, or a schema property outlive its field.
func TestCleanupSchemaPropertiesMatchGoTags(t *testing.T) {
	schema := readJSONFile(t, cleanupSchema).(map[string]any)
	visited := 0
	walkTyped(t, schema, schema, reflect.TypeFor[cleanup.Envelope](), nil, "envelope",
		func(path string, typ reflect.Type, node map[string]any, _ map[string]any) {
			visited++
			props, _ := node["properties"].(map[string]any)
			var gotProps, wantProps, gotReq, wantReq []string
			for k := range props {
				gotProps = append(gotProps, k)
			}
			for k, spec := range jsonFieldSpecs(t, typ) {
				wantProps = append(wantProps, k)
				if !spec.optional {
					wantReq = append(wantReq, k)
				}
			}
			if req, ok := node["required"].([]any); ok {
				for _, r := range req {
					gotReq = append(gotReq, r.(string))
				}
			}
			for _, s := range [][]string{gotProps, wantProps, gotReq, wantReq} {
				slices.Sort(s)
			}
			if !slices.Equal(gotProps, wantProps) {
				t.Errorf("%s (%s): schema properties %v, Go json fields %v", path, typ.Name(), gotProps, wantProps)
			}
			if !slices.Equal(gotReq, wantReq) {
				t.Errorf("%s (%s): schema required %v, Go always-written keys %v", path, typ.Name(), gotReq, wantReq)
			}
		})
	if visited < 5 {
		t.Errorf("only %d structs reached from Envelope; the walk is not following the schema", visited)
	}
}

// TestCleanupSchemaValidatorIsLoud is the validator's own test. Why: the
// contract test is only as good as its validator, so each failure it must
// catch is fed a bad case (extra property, missing required key, wrong type,
// misspelt enum value) and each schema form it does not model (an unmodelled
// keyword anywhere, even in a subschema no document reaches, a type list, a
// non-array enum, object additionalProperties, array-form items) must be an
// error, not skipped.
func TestCleanupSchemaValidatorIsLoud(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"type": "object", "required": []any{"a"}, "additionalProperties": false,
			"properties": map[string]any{
				"a": map[string]any{"type": "string", "enum": []any{"x", "y"}},
				"l": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			},
			"$defs": map[string]any{"unused": map[string]any{"type": "string"}},
		}
	}
	mod := func(f func(m map[string]any)) map[string]any { m := base(); f(m); return m }
	prop := func(m map[string]any, k string) map[string]any {
		return m["properties"].(map[string]any)[k].(map[string]any)
	}
	cases := []struct {
		name   string
		schema map[string]any
		doc    any
		want   string // substring of an error; "" means no error
	}{
		{"valid", base(), map[string]any{"a": "x"}, ""},
		{"extra property", base(), map[string]any{"a": "x", "b": 1.0}, `not allowed`},
		{"missing required", base(), map[string]any{}, `missing required`},
		{"wrong type", base(), map[string]any{"a": 1.0}, `type`},
		{"misspelt enum", base(), map[string]any{"a": "z"}, `not in enum`},
		{"unmodelled keyword", mod(func(m map[string]any) { m["minLength"] = 1.0 }), map[string]any{"a": "x"}, `unmodelled keyword`},
		{"unmodelled keyword in unreached $defs", mod(func(m map[string]any) {
			m["$defs"].(map[string]any)["unused"].(map[string]any)["pattern"] = "x"
		}), map[string]any{"a": "x"}, `unmodelled keyword`},
		{"unmodelled keyword in unreached property", mod(func(m map[string]any) { prop(m, "l")["items"].(map[string]any)["minimum"] = 0.0 }),
			map[string]any{"a": "x"}, `unmodelled keyword`},
		{"type list", mod(func(m map[string]any) { prop(m, "a")["type"] = []any{"string", "null"} }), map[string]any{"a": "x"}, `unsupported form`},
		{"enum not array", mod(func(m map[string]any) { prop(m, "a")["enum"] = "x" }), map[string]any{"a": "x"}, `unsupported form`},
		{"required not array", mod(func(m map[string]any) { m["required"] = "a" }), map[string]any{"a": "x"}, `unsupported form`},
		{"additionalProperties object", mod(func(m map[string]any) { m["additionalProperties"] = map[string]any{"type": "string"} }),
			map[string]any{"a": "x"}, `unsupported form`},
		{"items array form", mod(func(m map[string]any) { prop(m, "l")["items"] = []any{map[string]any{"type": "integer"}} }),
			map[string]any{"a": "x"}, `unsupported form`},
	}
	for _, c := range cases {
		errs := checkAgainst(c.schema, c.doc, "doc")
		switch {
		case c.want == "" && len(errs) > 0:
			t.Errorf("%s: unexpected errors %v", c.name, errs)
		case c.want != "" && !strings.Contains(strings.Join(errs, "\n"), c.want):
			t.Errorf("%s: errors %v lack %q", c.name, errs, c.want)
		}
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
