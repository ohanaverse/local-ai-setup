package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// startCall is what one engine call of a start carried.
type startCall struct{ replace, owed bool }

// stubLifecycleStartCalls replaces the engine with scripted outcomes (the nth
// call returns outcomes[n], nil past the end) and records AllowReplace and
// RestartOwed of each call.
func stubLifecycleStartCalls(t *testing.T, outcomes []error) *[]startCall {
	t.Helper()
	calls := &[]startCall{}
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		n := len(*calls)
		*calls = append(*calls, startCall{replace: opts.AllowReplace, owed: opts.RestartOwed})
		if n >= len(outcomes) {
			return nil
		}
		return outcomes[n]
	}
	t.Cleanup(func() { lifecycleStart = old })
	return calls
}

// TestStartForLaunchOwed verifies the start a profile launch uses. It passes
// the owed proxy restart to both engine attempts, and reports the restart as
// still owed only when it returns after a refusal it did not retry: then the
// engine touched nothing, and the caller must settle the restart itself. If
// it reported false there, the cloud routes written before the start would
// stay unpublished; if it reported true after the engine had restarted, the
// proxy would restart twice.
func TestStartForLaunchOwed(t *testing.T) {
	occupied := func() error {
		return &lifecycle.OccupiedError{Occupants: []localmodels.Entry{localmodelsEntry("omlx/other", "other")}}
	}
	unknown := func() error {
		return &lifecycle.OccupancyUnknownError{ProviderID: "mtplx", Origin: "http://localhost:8003"}
	}
	boom := errors.New("server exited")
	cases := []struct {
		name      string
		owed      bool
		replace   bool // --replace
		tty       bool
		answer    bool // the y/N answer when tty
		outcomes  []error
		wantCalls []startCall
		wantStill bool
		wantErr   string // "" means nil
	}{
		{"started at once", true, false, false, false, []error{nil}, []startCall{{false, true}}, false, ""},
		{"nothing owed is passed on as false", false, false, false, false, []error{nil}, []startCall{{false, false}}, false, ""},
		{"refused, replaced under --replace", true, true, false, false, []error{occupied(), nil}, []startCall{{false, true}, {true, true}}, false, ""},
		{"refused, confirmed at the prompt", true, false, true, true, []error{occupied(), nil}, []startCall{{false, true}, {true, true}}, false, ""},
		{"refused, declined at the prompt", true, false, true, false, []error{occupied()}, []startCall{{false, true}}, true, "cancelled — omlx/other still running"},
		{"refused, no terminal to ask on", true, false, false, false, []error{occupied()}, []startCall{{false, true}}, true, "--replace"},
		{"occupancy unknown, declined", true, false, true, false, []error{unknown()}, []startCall{{false, true}}, true, "cancelled — mtplx may still be serving a model"},
		{"refused and declined with nothing owed", false, false, true, false, []error{occupied()}, []startCall{{false, false}}, false, "cancelled"},
		{"any other failure", true, false, false, false, []error{boom}, []startCall{{false, true}}, false, "server exited"},
		{"replace granted, second attempt fails", true, true, false, false, []error{occupied(), boom}, []startCall{{false, true}, {true, true}}, false, "server exited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubSignals(t)
			stubStdinTTY(t, tc.tty)
			oldConfirm, oldOut := confirmReplace, osStderr
			confirmReplace = func(string) (bool, error) { return tc.answer, nil }
			osStderr = io.Discard
			t.Cleanup(func() { confirmReplace, osStderr = oldConfirm, oldOut })
			calls := stubLifecycleStartCalls(t, tc.outcomes)

			still, err := startForLaunchOwed(&config.Config{}, startTestRow(), tc.replace, tc.owed)

			if still != tc.wantStill {
				t.Errorf("stillOwed = %v, want %v", still, tc.wantStill)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if len(*calls) != len(tc.wantCalls) {
				t.Fatalf("engine calls = %+v, want %+v", *calls, tc.wantCalls)
			}
			for i, want := range tc.wantCalls {
				if (*calls)[i] != want {
					t.Errorf("engine call %d = %+v, want %+v", i, (*calls)[i], want)
				}
			}
		})
	}
}
