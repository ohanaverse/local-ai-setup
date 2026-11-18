// Package modelsetup is the admin-side setup of the model server: it reads
// and writes the host's wt-model LaunchAgent, config home and LiteLLM files.
// Everything it does to the machine goes through the seams of Env, so tests
// run the whole flow against fakes. It is on the admin side of the import
// graph (internal/depcheck): no session library package links it.
package modelsetup

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

const (
	// AgentLabel is the model server LaunchAgent's Label.
	AgentLabel = "local.wt-model.serve"
	// DefaultListen is the address the model server listens on by default.
	DefaultListen = "127.0.0.1:8787"
)

// FS is the file system the setup reads and writes.
type FS interface {
	ReadFile(path string) ([]byte, error)
	Stat(path string) (fs.FileInfo, error)
	WriteAtomic(path string, data []byte, perm os.FileMode) error
	Chmod(path string, perm os.FileMode) error
	Remove(path string) error
	// Backup copies path to a dated file beside it, with the same mode, and
	// returns the copy's path. It never replaces an earlier backup: a second
	// one in the same second gets a -2, -3, ... suffix.
	Backup(path string) (string, error)
}

// LaunchdJob is what launchctl reports for one job.
type LaunchdJob struct {
	Loaded, Running bool
	PID             int
}

// Launchd is the user's launchd domain (gui/<uid>).
type Launchd interface {
	Available() bool
	Print(ctx context.Context, label string) (LaunchdJob, error)
	Bootstrap(ctx context.Context, plistPath string) error
	Bootout(ctx context.Context, label string) error
	Kickstart(ctx context.Context, label string, kill bool) error
}

// ConfigStore is wt's config home: the [model-server] table and the LiteLLM
// proxy pair.
type ConfigStore interface {
	Read() config.ModelServerFile
	Update(change func(config.ModelServerState) (config.ModelServerState, error)) error
	Pair() (config.ProxyPair, error)
	SetPair(url, key *string) error
}

// Net is the network and process table as the setup sees them.
type Net interface {
	Ping(ctx context.Context, ep modelclient.Endpoint) (modelapi.Ping, error)
	Get(ctx context.Context, url, bearer string) (status int, body []byte, err error)
	ListenerPID(ctx context.Context, port int) (pid int, ok bool, err error)
	ProcessStart(ctx context.Context, pid int) (time.Time, error)
}

// Host is the process environment: variables, paths, the clock and entropy.
type Host interface {
	Getenv(string) string
	Home() (string, error)
	Executable() (string, error)
	LookPath(string) (string, error)
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
	RandRead([]byte) error
	GOOS() string
	UID() int
}

// LiteLLMFiles is where the LiteLLM proxy's plist and config.yaml live and
// what can be read from the plist.
type LiteLLMFiles interface {
	PlistPath() string
	ConfigPath() string
	ReadPlist(path string) (litellm.ProxyPlistInfo, error)
	PlistVar(path, name string) (value string, ok bool, err error)
	Binary() (path string, ok bool)
}

// Env bundles every seam. A zero field is a programming error; build one with
// DefaultEnv or a test's fake.
type Env struct {
	FS      FS
	Launchd Launchd
	Config  ConfigStore
	Net     Net
	Host    Host
	LiteLLM LiteLLMFiles
	// DesiredRoutes lists the model names the LiteLLM config should hold.
	DesiredRoutes func(ctx context.Context) ([]string, error)
}

// DefaultEnv is the real machine. Its launchctl, lsof and ps seams refuse in
// a test binary.
func DefaultEnv() Env {
	return Env{
		FS:      realFS{},
		Launchd: realLaunchd{},
		Config:  realConfig{},
		Net:     realNet{},
		Host:    realHost{},
		LiteLLM: realLiteLLM{},
	}
}

// errUnderTest is what the real launchctl, lsof and ps seams return in a test
// binary, so no test loads, unloads or inspects the developer's services.
var errUnderTest = errors.New("the machine's services and process table are not touched from a test binary")

type realFS struct{}

func (realFS) ReadFile(p string) ([]byte, error)   { return os.ReadFile(p) }
func (realFS) Stat(p string) (fs.FileInfo, error)  { return os.Stat(p) }
func (realFS) Chmod(p string, m os.FileMode) error { return os.Chmod(p, m) }
func (realFS) Remove(p string) error               { return os.Remove(p) }
func (realFS) WriteAtomic(p string, d []byte, m os.FileMode) error {
	return config.WriteFileAtomic(p, d, m)
}

// maxBackupTries bounds the -N suffixes Backup tries within one second.
const maxBackupTries = 1000

// backupCandidate is the n-th name a backup of p taken at t may use: the
// dated name, then the same with -2, -3, ...
func backupCandidate(p string, t time.Time, n int) string {
	name := p + ".bak-" + t.Format("20060102-150405")
	if n > 1 {
		name += "-" + strconv.Itoa(n)
	}
	return name
}

// backupName is the first candidate exists reports free, so a second backup
// in the same second never lands on the first (the fake FS uses it; the real
// one asks the file system with O_EXCL instead, which is also race-free).
func backupName(p string, t time.Time, exists func(string) bool) string {
	for n := 1; ; n++ {
		if c := backupCandidate(p, t, n); !exists(c) || n >= maxBackupTries {
			return c
		}
	}
}

// Backup copies p beside itself under the first free dated name, created
// with O_EXCL so an earlier backup is never replaced, at p's mode.
func (realFS) Backup(p string) (string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	perm := info.Mode().Perm()
	now := time.Now()
	for n := 1; n <= maxBackupTries; n++ {
		dst := backupCandidate(p, now, n)
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		serr := f.Sync()
		cerr := f.Close()
		merr := os.Chmod(dst, perm) // the umask may have narrowed it
		if err := errors.Join(werr, serr, cerr, merr); err != nil {
			return "", fmt.Errorf("the backup %s is incomplete (remove it by hand): %w", dst, err)
		}
		return dst, nil
	}
	return "", fmt.Errorf("no free backup name beside %s", p)
}

type realLaunchd struct{}

func (realLaunchd) Available() bool {
	if testing.Testing() {
		return false
	}
	_, err := exec.LookPath("launchctl")
	return runtime.GOOS == "darwin" && err == nil
}

func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// launchctl runs one launchctl command and returns its combined output.
func launchctl(ctx context.Context, args ...string) ([]byte, error) {
	if testing.Testing() {
		return nil, errUnderTest
	}
	out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("launchctl %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (realLaunchd) Print(ctx context.Context, label string) (LaunchdJob, error) {
	if testing.Testing() {
		return LaunchdJob{}, errUnderTest
	}
	out, err := exec.CommandContext(ctx, "launchctl", "print", domain()+"/"+label).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return LaunchdJob{}, nil // not loaded
		}
		return LaunchdJob{}, fmt.Errorf("launchctl print: %w", err)
	}
	job := LaunchdJob{Loaded: true}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "pid = "); ok {
			if pid, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && pid > 0 {
				job.PID, job.Running = pid, true
			}
		}
	}
	return job, nil
}

func (realLaunchd) Bootstrap(ctx context.Context, plistPath string) error {
	_, err := launchctl(ctx, "bootstrap", domain(), plistPath)
	return err
}

func (realLaunchd) Bootout(ctx context.Context, label string) error {
	_, err := launchctl(ctx, "bootout", domain()+"/"+label)
	return err
}

func (realLaunchd) Kickstart(ctx context.Context, label string, kill bool) error {
	args := []string{"kickstart"}
	if kill {
		args = append(args, "-k")
	}
	_, err := launchctl(ctx, append(args, domain()+"/"+label)...)
	return err
}

type realConfig struct{}

func (realConfig) Read() config.ModelServerFile { return config.ReadModelServerFile() }
func (realConfig) Update(change func(config.ModelServerState) (config.ModelServerState, error)) error {
	return config.UpdateModelServer(change)
}
func (realConfig) Pair() (config.ProxyPair, error) { return config.ResolveProxy() }
func (realConfig) SetPair(url, key *string) error  { return config.SetProxy(url, key) }

type realNet struct{}

func (realNet) Ping(ctx context.Context, ep modelclient.Endpoint) (modelapi.Ping, error) {
	return modelclient.New(ep).Ping(ctx)
}

// maxGetBody bounds what Get reads of a reply.
const maxGetBody = 1 << 20

func (realNet) Get(ctx context.Context, url, bearer string) (int, []byte, error) {
	client := &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGetBody))
	return resp.StatusCode, body, err
}

// findTool resolves a system tool: PATH first, then dir.
func findTool(name, dir string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	p := dir + "/" + name
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s not found on PATH or in %s", name, dir)
	}
	return p, nil
}

func (realNet) ListenerPID(ctx context.Context, port int) (int, bool, error) {
	if testing.Testing() {
		return 0, false, errUnderTest
	}
	lsof, err := findTool("lsof", "/usr/sbin")
	if err != nil {
		return 0, false, err
	}
	out, err := exec.CommandContext(ctx, lsof, "-nP", "-sTCP:LISTEN", "-iTCP:"+strconv.Itoa(port), "-F", "p").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(bytes.TrimSpace(out)) == 0 {
			return 0, false, nil // lsof exits 1 when nothing listens
		}
		return 0, false, fmt.Errorf("lsof: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "p"); ok {
			if pid, err := strconv.Atoi(v); err == nil && pid > 0 {
				return pid, true, nil
			}
		}
	}
	return 0, false, nil
}

func (realNet) ProcessStart(ctx context.Context, pid int) (time.Time, error) {
	if testing.Testing() {
		return time.Time{}, errUnderTest
	}
	ps, err := findTool("ps", "/bin")
	if err != nil {
		return time.Time{}, err
	}
	out, err := exec.CommandContext(ctx, ps, "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("ps: %w", err)
	}
	s := strings.Join(strings.Fields(string(out)), " ")
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("ps start time %q: %w", s, err)
	}
	return t, nil
}

type realHost struct{}

func (realHost) Getenv(k string) string            { return os.Getenv(k) }
func (realHost) Home() (string, error)             { return os.UserHomeDir() }
func (realHost) Executable() (string, error)       { return os.Executable() }
func (realHost) LookPath(n string) (string, error) { return exec.LookPath(n) }
func (realHost) Now() time.Time                    { return time.Now() }
func (realHost) RandRead(b []byte) error           { _, err := rand.Read(b); return err }
func (realHost) GOOS() string                      { return runtime.GOOS }
func (realHost) UID() int                          { return os.Getuid() }

func (realHost) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type realLiteLLM struct{}

func (realLiteLLM) PlistPath() string  { return litellm.ProxyPlistPath() }
func (realLiteLLM) ConfigPath() string { return litellm.DefaultPath() }
func (realLiteLLM) ReadPlist(p string) (litellm.ProxyPlistInfo, error) {
	return litellm.ReadProxyPlist(p)
}

// Binary finds the litellm binary: the install locations guide 14 and
// Homebrew use, then PATH. It never runs it, and finds nothing in a test
// binary so no test reads the developer's machine.
func (realLiteLLM) Binary() (string, bool) {
	if testing.Testing() {
		return "", false
	}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	for _, d := range dirs {
		p := filepath.Join(d, "litellm")
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	if p, err := exec.LookPath("litellm"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, true
		}
	}
	return "", false
}

func (realLiteLLM) PlistVar(p, n string) (string, bool, error) {
	return litellm.ReadProxyPlistVar(p, n)
}
