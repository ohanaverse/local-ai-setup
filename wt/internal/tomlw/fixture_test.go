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

// TestWrittenFixtureIsAFixedPoint is the writer contract: decoding the
// fixture and encoding it again reproduces every byte. The fixture was
// generated with tomli-w 1.2.0, so this also pins that wt's writer keeps that
// layout; no test runs tomli-w against the file any more, so regenerate it
// only with tomli_w.dumps. If this fails, a wt write that changes nothing
// would still rewrite the user's registry.
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
