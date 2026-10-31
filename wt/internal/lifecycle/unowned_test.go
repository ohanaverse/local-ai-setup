package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// unownedStart runs start for target on e with a snapshot showing target
// Running, and returns what the start printed and its error.
func unownedStart(e *env, cfg *config.Config, family, model string) (string, error) {
	snap := localmodels.Snapshot{
		Entries:   []localmodels.Entry{running(family, family+"/m", model)},
		Providers: map[string]localmodels.Status{family: localmodels.StatusOK},
	}
	e.inventory = func(*config.Config) localmodels.Snapshot { return snap }
	var out bytes.Buffer
	err := start(withRouteOutput(context.Background(), &out), e, cfg, Target{ProviderID: family, ModelName: model}, Options{})
	return out.String(), err
}

// TestStartWarnsWhenAForeignSushiAlreadyServesTheTarget verifies a server wt
// did not start (no pidfile) that already serves the target is left alone but
// reported: wt cannot see its --ctx-size or --max-tokens, so a long prompt
// may be refused, and the user needs to be told to stop it themselves. The
// start stays a no-op returning nil: nothing is spawned and nothing signalled.
func TestStartWarnsWhenAForeignSushiAlreadyServesTheTarget(t *testing.T) {
	pt, e, cfg, port := sigEnv(t, false)
	if err := os.Remove(e.serverProc["sushix"].pidfile); err != nil {
		t.Fatal(err)
	}
	if pidfile, record := filesLeftFor(e, "sushix"); pidfile || record {
		t.Fatalf("precondition: pidfile = %v, record = %v, want neither (a foreign server)", pidfile, record)
	}
	e.backends["sushix"] = processServer{"sushix"}
	e.spawn = func(pidProcess, string, []string) (*spawned, error) {
		t.Fatal("a server already serving the target must not be spawned over")
		return nil, nil
	}
	out, err := unownedStart(e, cfg, "sushix", "Org/Pack")
	if err != nil {
		t.Fatalf("start: %v, want nil (still a no-op)", err)
	}
	want := "wt: sushix is already serving Org/Pack on port " + strconv.Itoa(port) + " but wt did not start it; wt cannot see its flags (context size, max tokens), so a long prompt may be refused or truncated; stop it yourself to have wt start it with its own flags\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if len(pt.signals) != 0 {
		t.Errorf("signals = %v, want none", pt.signals)
	}
}

// TestStartDoesNotWarnForAServerWtStarted verifies a server whose pidfile and
// start record wt verified is wt's own, started with the flags wt chose, so
// there is nothing to warn about; a warning here would nag on every launch.
func TestStartDoesNotWarnForAServerWtStarted(t *testing.T) {
	_, e, cfg, _ := sigEnv(t, true)
	if pidfile, record := filesLeftFor(e, "sushix"); !pidfile || !record {
		t.Fatalf("precondition: pidfile = %v, record = %v, want both", pidfile, record)
	}
	e.backends["sushix"] = processServer{"sushix"}
	out, err := unownedStart(e, cfg, "sushix", "Org/Pack")
	if err != nil || out != "" {
		t.Errorf("err = %v, output = %q, want nil and no output", err, out)
	}
}

// TestStartDoesNotWarnForMtplx verifies the warning is for families with no
// stop command only: mtplx stops with its own command, wt reads its flags
// from nothing it needs, and its behaviour must not change.
func TestStartDoesNotWarnForMtplx(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{}, true, &calls)
	if pidfile, record := filesLeftFor(e, "mtplx"); pidfile || record {
		t.Fatalf("precondition: pidfile = %v, record = %v, want neither", pidfile, record)
	}
	out, err := unownedStart(e, &config.Config{}, "mtplx", "Org/M")
	if err != nil || out != "" || len(calls) != 0 {
		t.Errorf("err = %v, output = %q, calls = %v, want a silent no-op", err, out, calls)
	}
}

// TestStartAgainstAnIdleForeignSushiIsPortBusy pins existing behaviour: a
// sushi nobody loaded a pack into is not Running, so the start reaches the
// port check, finds /health answering, and refuses with *PortBusyError
// ("stop it") before spawning. wt never signals what it did not start, so no
// pidfile is created and nothing is signalled.
func TestStartAgainstAnIdleForeignSushiIsPortBusy(t *testing.T) {
	e, cfg, port, argvFile, _ := sushiEnv(t, "Org/Pack", 200, 0)
	if pidfile, record := filesLeftFor(e, "sushix"); pidfile || record {
		t.Fatalf("precondition: pidfile = %v, record = %v, want neither", pidfile, record)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	idle := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"Org/Pack","loaded":false}]}`))
		}
	}))
	idle.Listener.Close()
	idle.Listener = ln
	idle.Start()
	defer idle.Close()
	e.backends["sushix"] = processServer{"sushix"}
	e.signal = func(int, syscall.Signal) error {
		t.Error("wt must not signal a server it did not start")
		return nil
	}
	e.inventory = func(*config.Config) localmodels.Snapshot {
		return localmodels.Snapshot{Providers: map[string]localmodels.Status{"sushix": localmodels.StatusOK}}
	}
	err = start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"}, Options{})
	var pb *PortBusyError
	if !errors.As(err, &pb) || pb.Port != port || !strings.Contains(err.Error(), "stop it") {
		t.Fatalf("err = %v, want *PortBusyError on port %d naming stop it", err, port)
	}
	if _, statErr := os.Stat(argvFile); statErr == nil {
		t.Error("nothing should have been spawned")
	}
	if pidfile, record := filesLeftFor(e, "sushix"); pidfile || record {
		t.Errorf("after the refusal pidfile = %v, record = %v, want neither", pidfile, record)
	}
}
