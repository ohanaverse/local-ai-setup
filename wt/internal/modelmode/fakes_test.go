package modelmode

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// fakeEnv implements the four session seams (FS, Config, Net, Host) in
// memory. It records each call in order in calls (as "Seam.Method arg...";
// never file contents or secrets), and every canned result is a settable
// field. It is modelsetup's fake, cut to this package's seams: test helpers
// cannot cross packages, so each package carries its own.
type fakeEnv struct {
	t     *testing.T
	calls []string

	// FS: files and their modes.
	files    map[string][]byte
	modTimes map[string]time.Time // Stat's ModTime by path
	modes    map[string]os.FileMode
	writeErr map[string]error // WriteAtomic of this path fails, writing nothing
	// dangling holds paths that are a symlink whose target does not exist:
	// Lstat sees them, Stat and ReadFile report them absent.
	dangling map[string]bool

	// ConfigStore.
	configFile config.ModelServerFile
	// localTLS, when set, is what LocalTLS answers; nil reads the scratch
	// wt-model home through ReadLocalTLS (TestMain isolates it), so a test
	// that writes a [gateway] table there sees it. Not recorded: it is a
	// read of the host's TLS settings, never a change.
	localTLS  *LocalTLS
	updateErr error

	// Net.
	ping       modelapi.Ping
	pingErr    error
	pingErrs   []error  // consumed one per Ping before ping/pingErr apply
	pingTokens []string // the token of each Ping, in order

	// Host.
	env  map[string]string
	home string
}

// newFakeEnv returns a host with a fixed home and nothing configured.
func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	return &fakeEnv{
		t:        t,
		files:    map[string][]byte{},
		modes:    map[string]os.FileMode{},
		writeErr: map[string]error{},
		env:      map[string]string{},
		home:     "/Users/user_name",
	}
}

// Env returns the Env whose seams are this fake.
func (f *fakeEnv) Env() Env {
	return Env{FS: fakeFS{f}, Config: fakeConfig{f}, Net: fakeNet{f}, Host: fakeHost{f}}
}

func (f *fakeEnv) rec(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

// called reports whether any recorded call starts with prefix.
func (f *fakeEnv) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// mutations is the recorded calls that change something or reach the network
// or a secret: everything Observe should never do.
func (f *fakeEnv) mutations() []string {
	var out []string
	for _, c := range f.calls {
		for _, p := range []string{"FS.WriteAtomic", "FS.Chmod", "Config.Update", "Host.RandRead", "Net.Ping"} {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

type fakeFS struct{ f *fakeEnv }

type fakeInfo struct {
	name string
	size int
	mode os.FileMode
	mod  time.Time
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return int64(i.size) }
func (i fakeInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return i.mod }
func (i fakeInfo) IsDir() bool        { return false }
func (i fakeInfo) Sys() any           { return nil }

func (x fakeFS) ReadFile(p string) ([]byte, error) {
	x.f.rec("FS.ReadFile %s", p)
	d, ok := x.f.files[p]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), d...), nil
}

func (x fakeFS) Stat(p string) (fs.FileInfo, error) {
	x.f.rec("FS.Stat %s", p)
	d, ok := x.f.files[p]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}
	return fakeInfo{p, len(d), x.f.modes[p], x.f.modTimes[p]}, nil
}

func (x fakeFS) Lstat(p string) (fs.FileInfo, error) {
	x.f.rec("FS.Lstat %s", p)
	if x.f.dangling[p] {
		return fakeInfo{p, 0, os.ModeSymlink | 0o777, x.f.modTimes[p]}, nil
	}
	d, ok := x.f.files[p]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
	}
	return fakeInfo{p, len(d), x.f.modes[p], x.f.modTimes[p]}, nil
}

func (x fakeFS) WriteAtomic(p string, d []byte, m os.FileMode) error {
	x.f.rec("FS.WriteAtomic %s %04o", p, m)
	if err := x.f.writeErr[p]; err != nil {
		return err
	}
	x.f.files[p], x.f.modes[p] = append([]byte(nil), d...), m
	return nil
}

func (x fakeFS) Chmod(p string, m os.FileMode) error {
	x.f.rec("FS.Chmod %s %04o", p, m)
	if _, ok := x.f.files[p]; !ok {
		return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrNotExist}
	}
	x.f.modes[p] = m
	return nil
}

type fakeConfig struct{ f *fakeEnv }

func (x fakeConfig) LocalTLS() LocalTLS {
	if x.f.localTLS != nil {
		return *x.f.localTLS
	}
	return ReadLocalTLS(time.Now())
}

func (x fakeConfig) Read() config.ModelServerFile { x.f.rec("Config.Read"); return x.f.configFile }

func (x fakeConfig) Update(change func(config.ModelServerState) (config.ModelServerState, error)) error {
	x.f.rec("Config.Update")
	if x.f.updateErr != nil {
		return x.f.updateErr
	}
	var cur config.ModelServerState
	if x.f.configFile.Table != nil {
		cur = *x.f.configFile.Table
	}
	next, err := change(cur)
	if err != nil {
		return err
	}
	x.f.configFile.Exists, x.f.configFile.Table = true, &next
	return nil
}

type fakeNet struct{ f *fakeEnv }

func (x fakeNet) Ping(_ context.Context, ep modelping.Endpoint) (modelapi.Ping, modelping.Kind, string, error) {
	x.f.rec("Net.Ping %s", ep.Origin())
	x.f.pingTokens = append(x.f.pingTokens, ep.Token)
	var err error
	if len(x.f.pingErrs) > 0 {
		err = x.f.pingErrs[0]
		x.f.pingErrs = x.f.pingErrs[1:]
	} else {
		err = x.f.pingErr
	}
	p := x.f.ping
	if err == nil {
		return p, modelping.KindNone, "", nil
	}
	// The seam answers with the classification beside the error, exactly as
	// modelping.Checked does: the decoded ping beside a skew or schema error
	// carries the numbers the server spoke.
	o := modelping.Classify(err)
	if o.Kind == modelping.KindSkew {
		p.APIMajor = o.API
	}
	if o.Kind == modelping.KindSchema {
		p.Schema = o.Schema
	}
	return p, o.Kind, o.Message, err
}

type fakeHost struct{ f *fakeEnv }

func (x fakeHost) Getenv(k string) string { x.f.rec("Host.Getenv %s", k); return x.f.env[k] }
func (x fakeHost) Home() (string, error)  { x.f.rec("Host.Home"); return x.f.home, nil }
