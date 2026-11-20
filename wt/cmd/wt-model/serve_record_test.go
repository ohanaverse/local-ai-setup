package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
)

// testRuntime is a runtime whose record lives in a scratch directory, never
// the developer's wt-model home.
func testRuntime(t *testing.T) runtime {
	t.Helper()
	return runtime{Record: servestate.Store{Path: filepath.Join(t.TempDir(), "serve.json")}}
}

// listeningAddr parses the bound address out of serve's announce line.
func listeningAddr(t *testing.T, stderr string) string {
	t.Helper()
	re := regexp.MustCompile(`wt-model: listening on (\S+) \(gateway /v1, control /control/v1\)\n`)
	m := re.FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("no listening line; output %q", stderr)
	}
	return m[1]
}

// TestServeWritesTheRuntimeRecord pins that serve writes its runtime record
// before announcing: a host command finds the live server only through this
// record, and must never find the token in it. A clean stop removes it.
func TestServeWritesTheRuntimeRecord(t *testing.T) {
	seedConfig(t, true)
	rt := testRuntime(t)
	tok := writeToken(t, 0o600)
	readErr, _, stop := serveUntilListeningWith(t, rt, tok)
	addr := listeningAddr(t, readErr())

	rec, ok, err := rt.Record.Read()
	if !ok || err != nil {
		t.Fatalf("Read = ok %v err %v, want the record", ok, err)
	}
	if rec.PID != os.Getpid() || rec.Listen != addr || rec.TokenFile != tok || rec.Version != 1 || rec.Started.IsZero() {
		t.Fatalf("record = %+v, want pid %d listen %s token_file %s version 1 and a start time", rec, os.Getpid(), addr, tok)
	}
	fi, err := os.Stat(rt.Record.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %v, want 0600", fi.Mode().Perm())
	}
	raw, err := os.ReadFile(rt.Record.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("tok-abc")) {
		t.Fatalf("the record holds the token: %s", raw)
	}
	sessions, err := servestate.Sessions(t.Context(), rec)
	if err != nil || sessions == nil || len(sessions) != 0 {
		t.Fatalf("Sessions = %v, %v; want an empty map and nil", sessions, err)
	}

	if err := stop(); err != nil {
		t.Fatalf("serve returned %v, want nil", err)
	}
	if _, ok, err := rt.Record.Read(); ok || err != nil {
		t.Fatalf("after a clean stop Read = ok %v err %v, want no record", ok, err)
	}
}

// TestServeKeepsARecordAnotherServerWrote pins that a second serve overwrote
// the record, so the first one's clean exit must not delete it.
func TestServeKeepsARecordAnotherServerWrote(t *testing.T) {
	seedConfig(t, true)
	rt := testRuntime(t)
	tok := writeToken(t, 0o600)
	_, _, stop := serveUntilListeningWith(t, rt, tok)
	if err := rt.Record.Write(servestate.Record{PID: os.Getpid() + 1, Listen: "127.0.0.1:1", TokenFile: tok}); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v, want nil", err)
	}
	rec, ok, err := rt.Record.Read()
	if !ok || err != nil || rec.PID != os.Getpid()+1 {
		t.Fatalf("Read = %+v ok %v err %v, want the other server's record", rec, ok, err)
	}
}

// TestServeThatCannotListenLeavesTheRecordAlone pins that a serve that fails
// to bind neither writes nor removes the record a running server keeps.
func TestServeThatCannotListenLeavesTheRecordAlone(t *testing.T) {
	seedConfig(t, true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	rt := testRuntime(t)
	tok := writeToken(t, 0o600)
	if err := rt.Record.Write(servestate.Record{PID: os.Getpid() + 1, Listen: "127.0.0.1:1", TokenFile: tok}); err != nil {
		t.Fatal(err)
	}
	cmd := rootCmd(fakeDeps, rt)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"serve", "--listen", ln.Addr().String(), "--token-file", tok})
	err = cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "cannot listen on") {
		t.Fatalf("err = %v, want a cannot-listen error", err)
	}
	rec, ok, rerr := rt.Record.Read()
	if !ok || rerr != nil || rec.PID != os.Getpid()+1 {
		t.Fatalf("Read = %+v ok %v err %v, want the other server's record", rec, ok, rerr)
	}
}

// TestServeKeepsServingWhenTheRecordCannotBeWritten pins that a record that
// cannot be written is one stderr note, and serve goes on serving.
func TestServeKeepsServingWhenTheRecordCannotBeWritten(t *testing.T) {
	seedConfig(t, true)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := runtime{Record: servestate.Store{Path: filepath.Join(blocker, "serve.json")}}
	readErr, _, stop := serveUntilListeningWith(t, rt, writeToken(t, 0o600))
	stderr := readErr()
	if !strings.Contains(stderr, "wt-model: note: could not write the runtime record (") {
		t.Fatalf("stderr = %q, want the record note", stderr)
	}
	addr := listeningAddr(t, stderr)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer tok-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ping = %d, want 200", resp.StatusCode)
	}
	if err := stop(); err != nil {
		t.Fatalf("serve returned %v, want nil", err)
	}
}

// TestMainKeepsTheRecordOffTheRealHome pins that the TestMain isolation is
// what keeps servestate.Default() (the store main wires) inside a scratch home
// in this binary.
func TestMainKeepsTheRecordOffTheRealHome(t *testing.T) {
	p := servestate.Default().Path
	if p == "" {
		t.Fatal("Default().Path is empty")
	}
	if !strings.HasPrefix(p, os.Getenv("XDG_CONFIG_HOME")+string(filepath.Separator)) {
		t.Fatalf("Default().Path = %q, not under XDG_CONFIG_HOME %q", p, os.Getenv("XDG_CONFIG_HOME"))
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(os.Getenv("XDG_CONFIG_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, tmp+string(filepath.Separator)) {
		t.Fatalf("config home %q is not under the temp dir %q", dir, tmp)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, filepath.Join(home, ".config")) {
		t.Fatalf("Default().Path = %q is in the real config home", p)
	}
}
