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
	// The shim creates a marker before it sleeps, so the test sends the
	// Ctrl-C only once the child is running: a fixed sleep missed the child on
	// a loaded machine (the sharded pre-pr gate runs many test processes at
	// once), and the signal then reached only this binary. The marker is made
	// by the shell's own redirection, not by a `touch` child: a shell that is
	// waiting on a child which exits normally carries on after a SIGINT, so a
	// Ctrl-C that landed during `touch` would not stop the shim.
	marker := filepath.Join(t.TempDir(), "started")
	shimGit(t, ": > '"+marker+"'\nsleep 2\n")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	t.Cleanup(func() { signal.Stop(sig) })

	run := func(newGroup bool) error {
		_ = os.Remove(marker)
		g := cleanup.Git{Dir: t.TempDir(), Timeout: 10 * time.Second}
		g.NewProcessGroup = newGroup
		done := make(chan error, 1)
		go func() { done <- gitWrite(g, "status") }()
		// Let the child spawn, then send the terminal's Ctrl-C: wt's whole
		// foreground group receives it.
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the git shim never started")
			}
		}
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
