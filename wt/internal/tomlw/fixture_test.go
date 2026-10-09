package tomlw

import (
	"os"
	"testing"
)

// writtenFixture is the writer's contract fixture at the monorepo root: one
// registry in the exact form tomli-w gives it. It holds no comments, because
// tomli-w writes none, so what it is for is recorded here and in its readers:
// TestWrittenFixtureIsAFixedPoint below asserts wt's writer reproduces the
// same bytes, and llmbench/tests/test_registry.py and internal/config's
// TestTypedReaderLoadsTheWrittenFixture assert their readers load them.
const writtenFixture = "../../../docs/contracts/registry.written.sample.toml"

// TestWrittenFixtureIsAFixedPoint is the Go half of the writer contract:
// decoding the fixture and encoding it again reproduces every byte. modelman
// asserts the same of tomli-w on the same file, so the two writers are proved
// to agree without either CI job running the other language. If this fails,
// a wt write that changes nothing would still rewrite the user's registry.
func TestWrittenFixtureIsAFixedPoint(t *testing.T) {
	want, err := os.ReadFile(writtenFixture)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode(want)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got, err := Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decode-then-emit changed the fixture; got:\n%s", got)
	}
	again, err := Decode(got)
	if err != nil {
		t.Fatalf("the emitted text does not parse: %v", err)
	}
	if !Same(doc, again) {
		t.Error("the emitted text decodes to a different document")
	}
}
