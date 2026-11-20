package servestate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newStore returns a store whose record lives in a not-yet-made "home"
// directory under the test's temp dir.
func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Path: filepath.Join(t.TempDir(), "home", "serve.json")}
}

// newRecord returns the record the tests write, with a 0600 token file
// holding a known secret so a test can prove it never reaches the record.
func newRecord(t *testing.T) Record {
	t.Helper()
	tokPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokPath, []byte("tok-secret-xyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Record{PID: 4242, Listen: "127.0.0.1:8787", TokenFile: tokPath, Started: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
}

// writeRaw puts raw bytes at the store's path (0600), making its directory.
func writeRaw(t *testing.T, s Store, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestWriteThenReadRoundTrips checks that every field Write stores reads back
// unchanged, with the version set: a host command finds the server from it.
func TestWriteThenReadRoundTrips(t *testing.T) {
	s, rec := newStore(t), newRecord(t)
	if err := s.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, ok, err := s.Read()
	if err != nil || !ok {
		t.Fatalf("Read = ok %v, err %v; want ok, nil", ok, err)
	}
	if got.Version != RecordVersion || got.PID != 4242 || got.Listen != "127.0.0.1:8787" ||
		got.TokenFile != rec.TokenFile || !got.Started.Equal(rec.Started) {
		t.Fatalf("Read = %+v; want %+v with version %d", got, rec, RecordVersion)
	}
}

// TestWriteStoresVersionOneWhateverItIsGiven checks that the caller cannot
// write a record version the reader would refuse.
func TestWriteStoresVersionOneWhateverItIsGiven(t *testing.T) {
	s := newStore(t)
	if err := s.Write(Record{Version: 7, PID: 1}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _, err := s.Read()
	if err != nil || got.Version != 1 {
		t.Fatalf("Read = %+v, %v; want version 1", got, err)
	}
}

// TestWriteMakesA0600FileInA0700Directory checks the record's permissions: it
// names the token file, so only its owner may read it or its directory.
func TestWriteMakesA0600FileInA0700Directory(t *testing.T) {
	s := newStore(t)
	if err := s.Write(newRecord(t)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("home mode = %v, want 0700", di.Mode().Perm())
	}
}

// TestRecordNeverHoldsTheToken checks that the file holds the token's path,
// never the token, and exactly the five documented keys.
func TestRecordNeverHoldsTheToken(t *testing.T) {
	s := newStore(t)
	if err := s.Write(newRecord(t)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tok-secret-xyz") {
		t.Fatalf("record holds the token: %s", raw)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"listen", "pid", "started", "token_file", "version"}
	if !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

// TestReadAbsentIsNotAnError checks that no record (no server ran, or it
// exited cleanly) reads as absent, not as a failure.
func TestReadAbsentIsNotAnError(t *testing.T) {
	got, ok, err := newStore(t).Read()
	if got != (Record{}) || ok || err != nil {
		t.Fatalf("Read = %+v, %v, %v; want zero, false, nil", got, ok, err)
	}
}

// TestReadRefusesAMalformedRecord checks that a damaged record is an error
// naming the file, never a half-read record.
func TestReadRefusesAMalformedRecord(t *testing.T) {
	s := newStore(t)
	writeRaw(t, s, `{"pid":`)
	_, ok, err := s.Read()
	if ok || err == nil || !strings.Contains(err.Error(), s.Path) {
		t.Fatalf("Read = ok %v, err %v; want false and an error naming %s", ok, err, s.Path)
	}
}

// TestReadRefusesAnUnknownVersion checks that a record from another format
// version is refused with a message naming both versions.
func TestReadRefusesAnUnknownVersion(t *testing.T) {
	s := newStore(t)
	writeRaw(t, s, `{"version":2,"pid":1}`)
	_, ok, err := s.Read()
	want := s.Path + ": record version 2, want 1"
	if ok || err == nil || err.Error() != want {
		t.Fatalf("Read = ok %v, err %v; want %q", ok, err, want)
	}
}

// TestRemoveOnlyRemovesItsOwnPid checks that a server removes only the record
// it wrote, never one a later server (another pid) wrote over it.
func TestRemoveOnlyRemovesItsOwnPid(t *testing.T) {
	s := newStore(t)
	if err := s.Write(newRecord(t)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Remove(4243); err != nil {
		t.Fatalf("Remove(4243): %v", err)
	}
	got, ok, err := s.Read()
	if err != nil || !ok || got.PID != 4242 {
		t.Fatalf("after Remove(4243): Read = %+v, %v, %v; want pid 4242 kept", got, ok, err)
	}
	if err := s.Remove(4242); err != nil {
		t.Fatalf("Remove(4242): %v", err)
	}
	if _, ok, err := s.Read(); ok || err != nil {
		t.Fatalf("after Remove(4242): Read = ok %v, err %v; want absent", ok, err)
	}
}

// TestRemoveOfAnAbsentRecordIsNil checks that a shutdown with no record (its
// write failed, or someone removed it) is not an error.
func TestRemoveOfAnAbsentRecordIsNil(t *testing.T) {
	if err := newStore(t).Remove(1); err != nil {
		t.Fatalf("Remove = %v, want nil", err)
	}
}

// TestRemoveLeavesAMalformedRecord checks that Remove never deletes a file it
// cannot prove is its own record.
func TestRemoveLeavesAMalformedRecord(t *testing.T) {
	s := newStore(t)
	writeRaw(t, s, `{"pid":`)
	if err := s.Remove(1); err == nil {
		t.Fatal("Remove of a malformed record = nil, want an error")
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("malformed record was removed: %v", err)
	}
}

// TestEmptyStoreIsANoOp checks that a store with no path (no config home)
// writes, reads and removes nothing, in the temp dir or the cwd.
func TestEmptyStoreIsANoOp(t *testing.T) {
	dir := t.TempDir()
	list := func() []string {
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		return names
	}
	before := list()
	var s Store
	if err := s.Write(newRecord(t)); err != nil {
		t.Fatalf("Write = %v, want nil", err)
	}
	if got, ok, err := s.Read(); got != (Record{}) || ok || err != nil {
		t.Fatalf("Read = %+v, %v, %v; want zero, false, nil", got, ok, err)
	}
	if err := s.Remove(1); err != nil {
		t.Fatalf("Remove = %v, want nil", err)
	}
	if after := list(); !slices.Equal(before, after) {
		t.Fatalf("temp dir changed: %v -> %v", before, after)
	}
	if _, err := os.Stat("serve.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("serve.json in the cwd: %v", err)
	}
}

// TestDefaultIsInTheModelHome checks that the default store is serve.json in
// the wt-model home, and has no path when there is no config home.
func TestDefaultIsInTheModelHome(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	if got, want := Default().Path, filepath.Join(d, "wt-model", "serve.json"); got != want {
		t.Fatalf("Default().Path = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := Default().Path; got != "" {
		t.Fatalf("Default().Path with no home = %q, want empty", got)
	}
}

// TestWriteRefusesAPathOutsideTheTempDirUnderTest checks the test guard: a
// test can never write a record outside the temp dir.
func TestWriteRefusesAPathOutsideTheTempDirUnderTest(t *testing.T) {
	const p = "/wt-servestate-guard/serve.json"
	err := Store{Path: p}.Write(newRecord(t))
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("Write = %v; want an error naming %s", err, p)
	}
	if _, err := os.Stat("/wt-servestate-guard"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guard dir exists: %v", err)
	}
}
