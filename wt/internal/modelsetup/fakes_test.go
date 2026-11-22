package modelsetup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// fakeEnv implements every seam in memory. It records each call in order in
// calls (as "Seam.Method arg..."; never file contents or secrets), and every
// canned result is a settable field. Later tasks add fields as they need them.
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
	dangling  map[string]bool
	backupErr error

	// Launchd.
	launchdUnavailable bool
	jobs               map[string]LaunchdJob // by label
	launchdErr         error                 // returned by every mutating call
	bootstrapErr       error                 // returned by Bootstrap alone
	bootoutErr         error                 // returned by Bootout alone
	// bootstrapPID, when set, makes Bootstrap load and run the agent job with
	// this pid and Bootout unload it, so a later Observe sees the change.
	bootstrapPID int
	// loadedAfterBootout makes Print report the label still loaded for this
	// many calls after a successful Bootout (launchd's lag on a real machine).
	loadedAfterBootout int
	// lingerLabel limits loadedAfterBootout to one label ("" means every
	// label): a plan that unloads two jobs can then time out on one.
	lingerLabel string
	lingering   map[string]int
	// parents is each pid's parent for the fake ParentPID (absent: none).
	parents      map[int]int
	kickstartErr map[string]error // by label
	// litellmPID, when set, makes a Bootstrap of the LiteLLM plist load and
	// run the local.litellm.proxy job with this pid.
	litellmPID int

	// ConfigStore.
	configFile config.ModelServerFile
	pair       config.ProxyPair
	pairErr    error
	updateErr  error

	// Net.
	ping        modelapi.Ping
	pingErr     error
	pingErrs    []error  // consumed one per Ping before ping/pingErr apply
	pingTokens  []string // the token of each Ping, in order
	getStatus   int
	getBody     []byte
	getErr      error
	getReplies  map[string]getReply // by full URL; wins over getStatus/getBody
	listenerPID int
	listeners   map[int]int // pid listening by port; wins over the rest
	listenerOK  bool
	listenerErr error
	// listenerFromJob makes ListenerPID report the agent job's pid while it
	// runs (and nothing otherwise) instead of listenerPID/listenerOK.
	listenerFromJob bool
	processStart    time.Time
	processErr      error

	// Host.
	env      map[string]string
	home     string
	exe      string
	paths    map[string]string // LookPath results by name
	now      time.Time
	rand     byte // RandRead fills with this, incrementing
	goos     string
	uid      int
	sleepErr error

	// LiteLLMFiles.
	plistPath  string
	configPath string
	plistInfo  litellm.ProxyPlistInfo
	plistErr   error
	plistVars  map[string]string
	litellmBin string // what LiteLLMFiles.Binary finds; "" is none

	routes    []string
	routesErr error
}

// newFakeEnv returns a darwin host with a fixed clock, launchd available, and
// nothing configured.
func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	return &fakeEnv{
		t:            t,
		files:        map[string][]byte{},
		modes:        map[string]os.FileMode{},
		jobs:         map[string]LaunchdJob{},
		lingering:    map[string]int{},
		parents:      map[int]int{},
		writeErr:     map[string]error{},
		kickstartErr: map[string]error{},
		env:          map[string]string{},
		paths:        map[string]string{},
		plistVars:    map[string]string{},
		home:         "/Users/user_name",
		exe:          "/Users/user_name/.local/bin/wt-model",
		now:          time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
		goos:         "darwin",
		uid:          501,
		plistPath:    "/Users/user_name/Library/LaunchAgents/local.litellm.proxy.plist",
		configPath:   "/Users/user_name/.config/litellm/config.yaml",
	}
}

// Env returns the Env whose seams are this fake.
func (f *fakeEnv) Env() Env {
	return Env{
		FS: fakeFS{f}, Launchd: fakeLaunchd{f}, Config: fakeConfig{f},
		Net: fakeNet{f}, Host: fakeHost{f}, LiteLLM: fakeLiteLLM{f},
		DesiredRoutes: func(context.Context) ([]string, error) {
			f.rec("DesiredRoutes")
			return f.routes, f.routesErr
		},
	}
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

type fakeFS struct{ f *fakeEnv }

type fakeInfo struct {
	name string
	size int
	mode os.FileMode
	mod  time.Time
}

// getReply is one canned Net.Get answer.
type getReply struct {
	status int
	body   []byte
	err    error
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

func (x fakeFS) Remove(p string) error {
	x.f.rec("FS.Remove %s", p)
	if _, ok := x.f.files[p]; !ok {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
	}
	delete(x.f.files, p)
	delete(x.f.modes, p)
	return nil
}

func (x fakeFS) Backup(p, dir string) (string, bool, error) {
	x.f.rec("FS.Backup %s %s", p, dir)
	d, ok := x.f.files[p]
	if !ok {
		return "", false, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	if x.f.backupErr != nil {
		return "", false, x.f.backupErr
	}
	// The same naming rule as the real FS: in dir, never over an earlier backup.
	dst := backupName(filepath.Join(dir, filepath.Base(p)), x.f.now, func(c string) bool { _, ok := x.f.files[c]; return ok })
	// Like the real FS: the copy never has group or other bits.
	x.f.files[dst], x.f.modes[dst] = append([]byte(nil), d...), x.f.modes[p]&^0o077
	return dst, false, nil
}

// fileNames lists the in-memory files, sorted.
func (f *fakeEnv) fileNames() []string {
	var n []string
	for p := range f.files {
		n = append(n, p)
	}
	sort.Strings(n)
	return n
}

type fakeLaunchd struct{ f *fakeEnv }

func (x fakeLaunchd) Available() bool { x.f.rec("Launchd.Available"); return !x.f.launchdUnavailable }

func (x fakeLaunchd) Print(_ context.Context, label string) (LaunchdJob, error) {
	x.f.rec("Launchd.Print %s", label)
	if x.f.lingering[label] > 0 {
		x.f.lingering[label]--
		return LaunchdJob{Loaded: true}, x.f.launchdErr
	}
	return x.f.jobs[label], x.f.launchdErr
}

func (x fakeLaunchd) Bootstrap(_ context.Context, plist string) error {
	x.f.rec("Launchd.Bootstrap %s", plist)
	if x.f.bootstrapErr != nil {
		return x.f.bootstrapErr
	}
	if x.f.launchdErr == nil && x.f.bootstrapPID != 0 {
		x.f.jobs[AgentLabel] = LaunchdJob{Loaded: true, Running: true, PID: x.f.bootstrapPID}
	}
	if x.f.launchdErr == nil && x.f.litellmPID != 0 && plist == x.f.plistPath {
		x.f.jobs[litellm.ProxyLabel] = LaunchdJob{Loaded: true, Running: true, PID: x.f.litellmPID}
	}
	return x.f.launchdErr
}

func (x fakeLaunchd) Bootout(_ context.Context, label string) error {
	x.f.rec("Launchd.Bootout %s", label)
	if x.f.bootoutErr != nil {
		return x.f.bootoutErr
	}
	if x.f.launchdErr == nil {
		// A bootout launchd accepted unloads the job (after the lag
		// loadedAfterBootout models).
		delete(x.f.jobs, label)
		if x.f.lingerLabel == "" || x.f.lingerLabel == label {
			x.f.lingering[label] = x.f.loadedAfterBootout
		}
	}
	return x.f.launchdErr
}

func (x fakeLaunchd) Kickstart(_ context.Context, label string, kill bool) error {
	x.f.rec("Launchd.Kickstart %s kill=%t", label, kill)
	if err := x.f.kickstartErr[label]; err != nil {
		return err
	}
	return x.f.launchdErr
}

type fakeConfig struct{ f *fakeEnv }

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

func (x fakeConfig) Pair() (config.ProxyPair, error) {
	x.f.rec("Config.Pair")
	return x.f.pair, x.f.pairErr
}

func (x fakeConfig) SetPair(url, key *string) error {
	x.f.rec("Config.SetPair url=%t key=%t", url != nil, key != nil) // never the key itself
	if url != nil {
		x.f.pair.URL = *url
	}
	if key != nil {
		x.f.pair.APIKey = *key
	}
	return x.f.pairErr
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

func (x fakeNet) Get(_ context.Context, url, bearer string) (int, []byte, error) {
	x.f.rec("Net.Get %s bearer=%t", url, bearer != "")
	if r, ok := x.f.getReplies[url]; ok {
		return r.status, r.body, r.err
	}
	return x.f.getStatus, x.f.getBody, x.f.getErr
}

func (x fakeNet) ListenerPID(_ context.Context, port int) (int, bool, error) {
	x.f.rec("Net.ListenerPID %d", port)
	if pid, ok := x.f.listeners[port]; ok {
		return pid, pid != 0, nil
	}
	if x.f.listenerFromJob {
		if j := x.f.jobs[AgentLabel]; j.Running {
			return j.PID, true, nil
		}
		return 0, false, nil
	}
	return x.f.listenerPID, x.f.listenerOK, x.f.listenerErr
}

func (x fakeNet) ParentPID(_ context.Context, pid int) (int, error) {
	x.f.rec("Net.ParentPID %d", pid)
	return x.f.parents[pid], nil
}

func (x fakeNet) ProcessStart(_ context.Context, pid int) (time.Time, error) {
	x.f.rec("Net.ProcessStart %d", pid)
	return x.f.processStart, x.f.processErr
}

type fakeHost struct{ f *fakeEnv }

func (x fakeHost) Getenv(k string) string { x.f.rec("Host.Getenv %s", k); return x.f.env[k] }
func (x fakeHost) Home() (string, error)  { x.f.rec("Host.Home"); return x.f.home, nil }
func (x fakeHost) Executable() (string, error) {
	x.f.rec("Host.Executable")
	return x.f.exe, nil
}

func (x fakeHost) LookPath(n string) (string, error) {
	x.f.rec("Host.LookPath %s", n)
	if p, ok := x.f.paths[n]; ok {
		return p, nil
	}
	return "", &fs.PathError{Op: "lookpath", Path: n, Err: errors.New("not found")}
}

func (x fakeHost) Now() time.Time { return x.f.now } // not recorded: it is read constantly

// Sleep advances the fake clock by d instead of waiting.
func (x fakeHost) Sleep(ctx context.Context, d time.Duration) error {
	x.f.rec("Host.Sleep %s", d)
	if err := ctx.Err(); err != nil {
		return err
	}
	if x.f.sleepErr != nil {
		return x.f.sleepErr
	}
	x.f.now = x.f.now.Add(d)
	return nil
}

func (x fakeHost) RandRead(b []byte) error {
	x.f.rec("Host.RandRead %d", len(b))
	for i := range b {
		b[i] = x.f.rand
		x.f.rand++
	}
	return nil
}

func (x fakeHost) GOOS() string { return x.f.goos }
func (x fakeHost) UID() int     { return x.f.uid }

type fakeLiteLLM struct{ f *fakeEnv }

func (x fakeLiteLLM) PlistPath() string  { return x.f.plistPath }
func (x fakeLiteLLM) ConfigPath() string { return x.f.configPath }

func (x fakeLiteLLM) Binary() (string, bool) {
	x.f.rec("LiteLLM.Binary")
	return x.f.litellmBin, x.f.litellmBin != ""
}

func (x fakeLiteLLM) ReadPlist(p string) (litellm.ProxyPlistInfo, error) {
	x.f.rec("LiteLLM.ReadPlist %s", p)
	return x.f.plistInfo, x.f.plistErr
}

func (x fakeLiteLLM) PlistVar(p, n string) (string, bool, error) {
	x.f.rec("LiteLLM.PlistVar %s %s", p, n)
	v, ok := x.f.plistVars[n]
	return v, ok, x.f.plistErr
}

// TestFakeEnvRecordsCallsInOrderAndAdvancesTheClock pins the helper later
// tasks rely on: calls are recorded in order, never with secrets, and Sleep
// moves Now instead of waiting.
func TestFakeEnvRecordsCallsInOrderAndAdvancesTheClock(t *testing.T) {
	f := newFakeEnv(t)
	env := f.Env()
	ctx := context.Background()
	start := env.Host.Now()
	_ = env.Host.Sleep(ctx, 3*time.Second)
	_ = env.FS.WriteAtomic("/x", []byte("secret"), 0o600)
	key := "sk-secret"
	_ = env.Config.SetPair(nil, &key)
	if got := env.Host.Now().Sub(start); got != 3*time.Second {
		t.Errorf("clock advanced %v, want 3s", got)
	}
	want := []string{"Host.Sleep 3s", "FS.WriteAtomic /x 0600", "Config.SetPair url=false key=true"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}
