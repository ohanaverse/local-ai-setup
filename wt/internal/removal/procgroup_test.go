package removal

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// shimGit writes a git-named shell script into a PATH directory ahead of the
// real one: cleanup.Git runs the literal "git", so the shim stands in for the
// child the test wants to control. The removal package's tests run serially,
// so the PATH swap is contained.
func shimGit(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// TestGitWriteNamesASignalDeath pins #558's second half: a git child that
// dies by a signal is named in the error, not "git exit -1" — the message is
// what tells the user what happened to the root's reset or switch.
func TestGitWriteNamesASignalDeath(t *testing.T) {
	shimGit(t, "kill -INT $$\n")
	g := cleanup.Git{Dir: t.TempDir(), Timeout: 10 * time.Second}

	err := gitWrite(g, "status")
	if err == nil {
		t.Fatal("the killed git child was not an error")
	}
	if !strings.Contains(err.Error(), "interrupt") {
		t.Fatalf("the signal death is not named: %v", err)
	}
}

// TestGitWriteRunsInItsOwnProcessGroup pins #558's first half: a write in
// its own process group survives the terminal's Ctrl-C, which reaches the
// whole foreground group — ReleaseRoot's reset and switch must not die
// mid-write that way. A write left in the foreground group dies with it.
func TestGitWriteRunsInItsOwnProcessGroup(t *testing.T) {
	// The test first moves itself into its own process group: the group
	// SIGINT below must reach this test binary and its same-group children
	// only, never the go tool or the shell that ran it.
	if err := syscall.Setpgid(0, 0); err != nil {
		t.Skipf("could not detach the test into its own process group: %v", err)
	}
	shimGit(t, "sleep 2\n")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	t.Cleanup(func() { signal.Stop(sig) })

	run := func(newGroup bool) error {
		g := cleanup.Git{Dir: t.TempDir(), Timeout: 10 * time.Second}
		g.NewProcessGroup = newGroup
		done := make(chan error, 1)
		go func() { done <- gitWrite(g, "status") }()
		// Let the child spawn, then send the terminal's Ctrl-C: wt's whole
		// foreground group receives it.
		time.Sleep(400 * time.Millisecond)
		if err := syscall.Kill(-syscall.Getpgrp(), syscall.SIGINT); err != nil {
			t.Fatalf("kill: %v", err)
		}
		return <-done
	}
	if err := run(true); err != nil {
		t.Fatalf("a write in its own process group did not survive the terminal's Ctrl-C: %v", err)
	}
	select {
	case <-sig:
	default:
	}
	if err := run(false); err == nil {
		t.Fatal("a write left in the foreground group survived the Ctrl-C it dies with")
	}
	select {
	case <-sig:
	default:
	}
}
