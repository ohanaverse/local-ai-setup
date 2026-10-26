package main

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// modelListFixture is `wt model list --json`'s contract fixture at the
// monorepo root: one document in the shape the command prints. modeltiers
// reads its inventory from that command and from nothing else, and its tests
// read this same file (modeltiers/tests/conftest.py, INVENTORY), so the tests
// below are what stops a renamed key here from passing both suites and then
// failing the first real `modeltiers run`.
const modelListFixture = "../../../docs/contracts/model-list.sample.json"

// jsonFieldSpec is one struct field as encoding/json writes it.
type jsonFieldSpec struct {
	typ reflect.Type
	// optional: the key is left out for a zero value (omitempty, omitzero).
	optional bool
}

// jsonFieldSpecs returns typ's fields by the key encoding/json gives each.
// It fails on anything the shape check below does not model (an embedded
// struct, an unexported field), so a new kind of field cannot go unchecked.
func jsonFieldSpecs(t *testing.T, typ reflect.Type) map[string]jsonFieldSpec {
	t.Helper()
	specs := map[string]jsonFieldSpec{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Anonymous || !f.IsExported() {
			t.Fatalf("%s.%s: embedded and unexported fields are not modelled by this contract test", typ, f.Name)
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" && opts == "" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		options := strings.Split(opts, ",")
		specs[name] = jsonFieldSpec{
			typ:      f.Type,
			optional: slices.Contains(options, "omitempty") || slices.Contains(options, "omitzero"),
		}
	}
	return specs
}

// rawJSONKind names the JSON type of one encoded value.
func rawJSONKind(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "nothing"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}

// checkJSONShape reports every place where raw is not a value encoding/json
// could have written for typ: a key the type does not write, a key the type
// always writes that is absent, or a value of another JSON type. Only a
// pointer field accepts null, because a string, a bool, a slice built with
// append([]string{}, ...) and a map made with a literal never encode as null.
// The match is on the exact key: encoding/json's own decoder folds case, and
// a reader in another language does not.
func checkJSONShape(t *testing.T, path string, raw json.RawMessage, typ reflect.Type) {
	t.Helper()
	kind := rawJSONKind(raw)
	if typ.Kind() == reflect.Pointer {
		if kind == "null" {
			return
		}
		typ = typ.Elem()
	}
	want := ""
	switch typ.Kind() {
	case reflect.Struct, reflect.Map:
		want = "object"
	case reflect.Slice:
		want = "array"
	case reflect.String:
		want = "string"
	case reflect.Bool:
		want = "boolean"
	case reflect.Int, reflect.Int64, reflect.Float64:
		want = "number"
	default:
		t.Fatalf("%s: this contract test does not model a Go %s", path, typ)
	}
	if kind != want {
		t.Errorf("%s is a JSON %s; wt writes a %s (Go %s)", path, kind, want, typ)
		return
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		specs := jsonFieldSpecs(t, typ)
		for _, key := range slices.Sorted(maps.Keys(object)) {
			spec, ok := specs[key]
			if !ok {
				t.Errorf("%s.%s is in the fixture, and %s writes no such key", path, key, typ)
				continue
			}
			checkJSONShape(t, path+"."+key, object[key], spec.typ)
		}
		for _, key := range slices.Sorted(maps.Keys(specs)) {
			if _, ok := object[key]; !ok && !specs[key].optional {
				t.Errorf("%s.%s is missing from the fixture, and %s always writes it", path, key, typ)
			}
		}
	case reflect.Map:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, key := range slices.Sorted(maps.Keys(object)) {
			checkJSONShape(t, path+"."+key, object[key], typ.Elem())
		}
	case reflect.Slice:
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for i, element := range array {
			checkJSONShape(t, path+"["+strconv.Itoa(i)+"]", element, typ.Elem())
		}
	}
}

// readModelListFixture returns the contract fixture's bytes.
func readModelListFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(modelListFixture)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// modelRowKeys returns, for a `wt model list --json` document, the keys some
// row carries and the keys every row carries.
func modelRowKeys(t *testing.T, raw []byte) (some, every []string) {
	t.Helper()
	var doc struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not a model list document: %v\n%s", err, raw)
	}
	if len(doc.Models) == 0 {
		t.Fatalf("the document has no model rows, so it pins no row shape:\n%s", raw)
	}
	count := map[string]int{}
	for _, row := range doc.Models {
		for key := range row {
			count[key]++
		}
	}
	for _, key := range slices.Sorted(maps.Keys(count)) {
		some = append(some, key)
		if count[key] == len(doc.Models) {
			every = append(every, key)
		}
	}
	return some, every
}

// TestModelListFixtureMatchesTheDocumentTypes pins the shared fixture
// (docs/contracts/model-list.sample.json) to modelListJSON and modelListRow,
// the two types `wt model list --json` encodes, in both directions: every key
// the fixture's document and rows carry is one the types write, with the JSON
// type they write it as, and every key the types always write is in the
// fixture's document and in each of its rows. An omitempty key (target,
// draft) may be absent and a pointer field (size_bytes, path) may be null.
// The file must also decode into the types with unknown keys refused and
// encode back to the same document.
//
// modeltiers reads its inventory only from this command, and its tests read
// this file. Without this test a json tag renamed in modelListRow
// (provider_id, location, registered) passes wt's suite, passes modeltiers'
// suite against a fixture that still has the old key, and the first real
// `modeltiers run` stops with an InventoryError. A key added to or renamed in
// either type must reach the fixture in the same change.
func TestModelListFixtureMatchesTheDocumentTypes(t *testing.T) {
	raw := readModelListFixture(t)
	checkJSONShape(t, "document", raw, reflect.TypeFor[modelListJSON]())
	// A fixture with no rows would pass the check above and pin no row.
	modelRowKeys(t, raw)

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc modelListJSON
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("the fixture does not decode into modelListJSON: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("the fixture holds more than one document (next token error = %v)", err)
	}
	again, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(again, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoding the fixture and encoding it again changed it; wt would write:\n%s", again)
	}
}

// TestModelListJSONOutputMatchesTheFixture compares the fixture with what
// the command really prints, without going through the Go types: the
// document `runModelList` writes for a registry with a cloud model, local
// models with and without a size and a path, a discovered model and a
// pairing has the fixture's top-level keys, and its rows carry the fixture's
// row keys. The one difference allowed is a key the row type marks omitempty
// (target and draft, which the fixture's rows do not have and a pairing's row
// does, and serve_args, which only an mtplx row with serve_args does). The printed document also passes the same shape check the fixture
// passes.
//
// The types test above trusts that the command encodes modelListJSON as it
// stands; this one fails if the command stops doing so (a map built by hand,
// a custom MarshalJSON, a second document type), which would leave the
// fixture describing a shape modeltiers no longer receives.
func TestModelListJSONOutputMatchesTheFixture(t *testing.T) {
	cfg, snap := modelListServeArgsConfig()
	stubProbeInventory(t, snap)
	var out, errOut bytes.Buffer
	if err := runModelList(&out, &errOut, cfg, true, 80); err != nil {
		t.Fatal(err)
	}
	checkJSONShape(t, "output", out.Bytes(), reflect.TypeFor[modelListJSON]())

	fixture := readModelListFixture(t)
	if got, want := jsonKeySet(t, out.Bytes()), jsonKeySet(t, fixture); !slices.Equal(got, want) {
		t.Errorf("document keys = %v, fixture = %v", got, want)
	}
	var optional []string
	for key, spec := range jsonFieldSpecs(t, reflect.TypeFor[modelListRow]()) {
		if spec.optional {
			optional = append(optional, key)
		}
	}
	slices.Sort(optional)
	without := func(keys []string) []string {
		return slices.DeleteFunc(slices.Clone(keys), func(key string) bool { return slices.Contains(optional, key) })
	}
	gotSome, gotEvery := modelRowKeys(t, out.Bytes())
	wantSome, wantEvery := modelRowKeys(t, fixture)
	if !slices.Equal(gotEvery, wantEvery) {
		t.Errorf("keys every printed row has = %v, keys every fixture row has = %v", gotEvery, wantEvery)
	}
	if got, want := without(gotSome), without(wantSome); !slices.Equal(got, want) {
		t.Errorf("row keys printed (omitempty ones aside) = %v, fixture = %v", got, want)
	}
	// The sample registry has a pairing, so the optional keys are printed
	// here: were they not, the comparison above would say nothing about them.
	for _, key := range optional {
		if !slices.Contains(gotSome, key) {
			t.Errorf("no printed row carries the omitempty key %q; the sample registry no longer exercises it", key)
		}
	}
}
