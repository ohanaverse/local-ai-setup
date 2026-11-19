package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The JSON shape helpers wt-session's contract tests use (cleanup, remove,
// rescue, restore-root and the host commands). They were in
// model_list_contract_test.go, which moved to cmd/wt-model with
// `wt-model model list` (Task 37); cmd/wt-model keeps its own copy there.

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
