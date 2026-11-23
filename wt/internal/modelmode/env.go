package modelmode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// FS is the file system the session side reads and writes. modelsetup widens
// it with Remove and Backup; the five methods here are what a mode plan and
// an observation share.
type FS interface {
	ReadFile(path string) ([]byte, error)
	Stat(path string) (fs.FileInfo, error)
	// Lstat is Stat without following a final symlink: a dangling link is
	// there for Lstat and absent for Stat.
	Lstat(path string) (fs.FileInfo, error)
	WriteAtomic(path string, data []byte, perm os.FileMode) error
	Chmod(path string, perm os.FileMode) error
}

// ConfigStore is wt's config home: the [model-server] table, and the
// wt-model home's TLS settings a local url follows (read only: no code
// writes them). modelsetup widens it with the LiteLLM proxy pair.
type ConfigStore interface {
	Read() config.ModelServerFile
	Update(change func(config.ModelServerState) (config.ModelServerState, error)) error
	LocalTLS() LocalTLS
}

// Net is a ping of the model server: the ping answer beside its
// classification (kind and message) and the error unchanged, so a caller can
// still errors.As it. modelsetup widens it with the listener and process
// probes.
type Net interface {
	Ping(ctx context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error)
}

// Host is the process environment the session side reads.
type Host interface {
	Getenv(string) string
	Home() (string, error)
}

// Env bundles the session-safe seams. A zero field is a programming error;
// build one with DefaultEnv or a test's fake. modelsetup's Env embeds these
// four values through its Mode() view.
type Env struct {
	FS     FS
	Config ConfigStore
	Net    Net
	Host   Host
}

// DefaultEnv is the real machine, under the exported seam types.
func DefaultEnv() Env {
	return Env{FS: RealFS{}, Config: RealConfig{}, Net: RealNet{}, Host: RealHost{}}
}

// ErrOutsideTempUnderTest is what a real file seam returns in a test binary
// for a path outside os.TempDir(), so no test changes the developer's files.
// Exported so modelsetup's tests keep errors.Is against it.
var ErrOutsideTempUnderTest = errors.New("a test binary changes no file outside the temp directory")

// errHomeUnderTest is what the real home seam returns in a test binary
// whose HOME is not inside os.TempDir().
var errHomeUnderTest = errors.New("a test binary uses no home directory outside the temp directory (set HOME to a temp dir)")

// GuardUnderTest refuses, in a test binary only, any path that is not inside
// os.TempDir() (as given, or with its symlinks resolved).
func GuardUnderTest(paths ...string) error {
	if !testing.Testing() {
		return nil
	}
	roots := []string{filepath.Clean(os.TempDir())}
	if r, err := filepath.EvalSymlinks(roots[0]); err == nil {
		roots = append(roots, r)
	}
	for _, p := range paths {
		p = filepath.Clean(p)
		inside := false
		for _, r := range roots {
			if strings.HasPrefix(p, r+string(filepath.Separator)) {
				inside = true
			}
		}
		if !inside {
			return fmt.Errorf("%s: %w", p, ErrOutsideTempUnderTest)
		}
	}
	return nil
}

// RealFS is the real file system seam.
type RealFS struct {
	// Resolve resolves a path's symlinks: filepath.EvalSymlinks when nil. A
	// test sets it to check the guard on a resolved path outside the temp
	// directory without making a link that leaves it.
	Resolve func(string) (string, error)
}

func (x RealFS) evalSymlinks(p string) (string, error) {
	if x.Resolve != nil {
		return x.Resolve(p)
	}
	return filepath.EvalSymlinks(p)
}

// GuardAncestorUnderTest guards, in a test binary only, p with its nearest
// existing ancestor's symlinks resolved, so a write, removal or backup under
// a linked directory that leaves the temp directory is refused before it
// happens (GuardUnderTest alone sees only the path as given).
func (x RealFS) GuardAncestorUnderTest(p string) error {
	if !testing.Testing() {
		return nil
	}
	p = filepath.Clean(p)
	for dir, rest := filepath.Dir(p), filepath.Base(p); ; dir, rest = filepath.Dir(dir), filepath.Join(filepath.Base(dir), rest) {
		if real, err := x.evalSymlinks(dir); err == nil {
			return GuardUnderTest(filepath.Join(real, rest))
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

func (RealFS) ReadFile(p string) ([]byte, error)   { return os.ReadFile(p) }
func (RealFS) Stat(p string) (fs.FileInfo, error)  { return os.Stat(p) }
func (RealFS) Lstat(p string) (fs.FileInfo, error) { return os.Lstat(p) }
func (x RealFS) Chmod(p string, m os.FileMode) error {
	if err := GuardUnderTest(p); err != nil {
		return err
	}
	// Chmod follows a link, so in a test binary the path it resolves to is
	// guarded too (one that does not resolve fails os.Chmod on its own).
	if testing.Testing() {
		if real, err := x.evalSymlinks(p); err == nil {
			if err := GuardUnderTest(real); err != nil {
				return err
			}
		}
	}
	return os.Chmod(p, m)
}

func (x RealFS) WriteAtomic(p string, d []byte, m os.FileMode) error {
	if err := GuardUnderTest(p); err != nil {
		return err
	}
	if err := x.GuardAncestorUnderTest(p); err != nil {
		return err
	}
	// A rename over a symlink would replace the link with a regular file, so
	// a plist a dotfiles tool links into place is written where it points. A
	// link that does not resolve (dangling, a loop, an unreadable parent) is
	// refused: replacing it would put the content where the user did not mean
	// it to go. The resolved path is guarded again in a test binary.
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		real, err := x.evalSymlinks(p)
		if err != nil {
			return fmt.Errorf("%s is a symlink that does not resolve; nothing was written", p)
		}
		if err := GuardUnderTest(real); err != nil {
			return err
		}
		p = real
	}
	return config.WriteFileAtomic(p, d, m)
}

// RealConfig is the real config home seam.
type RealConfig struct{}

func (RealConfig) Read() config.ModelServerFile { return config.ReadModelServerFile() }

// LocalTLS reads the wt-model home's [gateway] TLS settings at the current
// time.
func (RealConfig) LocalTLS() LocalTLS { return ReadLocalTLS(time.Now()) }
func (RealConfig) Update(change func(config.ModelServerState) (config.ModelServerState, error)) error {
	return config.UpdateModelServer(change)
}

// RealNet pings the real model server.
type RealNet struct{}

func (RealNet) Ping(ctx context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error) {
	return modelping.Checked(ctx, ep)
}

// RealHost is the real process environment seam.
type RealHost struct{}

func (RealHost) Getenv(k string) string { return os.Getenv(k) }

// Home is the user's home directory. In a test binary it is refused unless
// HOME was pointed inside os.TempDir(), so a test that reaches DefaultEnv
// never plans or writes under the developer's home.
func (RealHost) Home() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil || !testing.Testing() {
		return h, err
	}
	if GuardUnderTest(h) != nil {
		return "", errHomeUnderTest
	}
	return h, nil
}

// DanglingLink is true when p is a symlink (Lstat sees it) that Stat cannot
// follow: its target does not exist, or the links loop. Whatever the reason,
// a write there would replace the link, so it reads as present, unreadable.
func DanglingLink(f FS, p string) bool {
	fi, err := f.Lstat(p)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	_, err = f.Stat(p)
	return err != nil
}
