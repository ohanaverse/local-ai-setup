package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// readyState is one answer to "is the spawned server ready": Model is the
// model the server says it serves, when its readiness check names one;
// Endpoint is the URL the instance says it listens at, read from the spec's
// Ready.EndpointPath ("" when the spec names no path or the answer lacks it).
type readyState struct {
	Ready    bool
	Model    string
	Endpoint string
}

// readyNow asks once whether a process family's server is ready to serve
// model, the way its spec's Ready says.
//
// ReadyModelsList: ready when modelsURL lists the model (lenient name match);
// a keyed request the server refuses is a *KeyRefusedError.
//
// ReadyCommandJSON: bin runs with spec.Ready.Command, bounded by
// e.readyCmdTimeout; its output must be one JSON object whose value at the
// dot-separated Path renders as Equals. A failed command, empty or invalid
// output, a missing path or a timeout is not ready with a nil error — a
// server still loading answers exactly that way (rec exits 1 while nothing
// is reachable) and the wait goes on.
func (e *env) readyNow(ctx context.Context, spec provider.ProcessSpec, bin, modelsURL, model, key string) (readyState, error) {
	switch spec.Ready.Kind {
	case provider.ReadyModelsList:
		ids, err := localmodels.FetchModelIDsKeyed(e.probeClient, modelsURL, key)
		if key != "" && errors.Is(err, localmodels.ErrKeyRefused) {
			return readyState{}, &KeyRefusedError{URL: modelsURL, KeySent: true, Detail: err.Error()}
		}
		for _, id := range ids {
			if localmodels.NameMatches(id, model) {
				return readyState{Ready: true, Model: id}, nil
			}
		}
		return readyState{}, nil
	case provider.ReadyCommandJSON:
		rctx, cancel := context.WithTimeout(ctx, e.readyCmdTimeout)
		defer cancel()
		out, err := e.run(rctx, bin, spec.Ready.Command...)
		if err != nil || rctx.Err() != nil {
			return readyState{}, nil
		}
		var doc map[string]any
		dec := json.NewDecoder(bytes.NewReader(out))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil || doc == nil {
			return readyState{}, nil
		}
		got, ok := jsonScalarAt(doc, spec.Ready.Path)
		if !ok || got != spec.Ready.Equals {
			return readyState{}, nil
		}
		st := readyState{Ready: true}
		if spec.Ready.ModelPath != "" {
			st.Model, _ = jsonScalarAt(doc, spec.Ready.ModelPath)
		}
		if spec.Ready.EndpointPath != "" {
			st.Endpoint, _ = jsonScalarAt(doc, spec.Ready.EndpointPath)
		}
		return st, nil
	default:
		return readyState{}, fmt.Errorf("unknown readiness kind %q", spec.Ready.Kind)
	}
}

// jsonScalarAt is the scalar at a dot-separated path in doc rendered as
// text: a string as itself, a bool as "true"/"false", a number as written.
// A missing key, a null, an object or an array is false.
func jsonScalarAt(doc map[string]any, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	var cur any = doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[seg]; !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

// waitReady waits for family's spawned server to be ready to serve model.
// A ReadyModelsList family waits exactly as waitForModel always has (its
// timing and errors unchanged). Any other kind polls readyNow every
// pollInterval: nil once ready; "serve process exited during model load"
// the moment sp has exited; a server whose port is still closed while its
// process lives is loading, not failed, and is waited for; a ready answer
// naming another model, or none when the spec reads one, is not ready; at timeout an error naming the family
// (and the other model, when one was seen); a ctx error as itself.
func (e *env) waitReady(ctx context.Context, family string, sp procWatch, bin, modelsURL, model, key string, timeout time.Duration) error {
	spec, ok := processSpecFor(family)
	if !ok {
		return &UnsupportedError{ProviderID: family}
	}
	if spec.Ready.Kind == provider.ReadyModelsList {
		return e.waitForModel(ctx, modelsURL, model, sp, key, timeout)
	}
	deadline := time.Now().Add(timeout)
	// other is the model a ready instance named when it was not model: a
	// ScopeMachine family's readiness command answers for whatever instance
	// holds the machine, which need not be the one wt spawned. That is not
	// ready; the wait goes on, so a server that refuses the second instance
	// still ends it through sp.exited().
	other := ""
	// unnamed: a ready answer named no model although the spec reads one.
	unnamed := false
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if sp != nil {
			if done, err := sp.exited(); done {
				return fmt.Errorf("serve process exited during model load: %v", err)
			}
		}
		st, err := e.readyNow(ctx, spec, bin, modelsURL, model, key)
		if err != nil {
			return err
		}
		switch {
		case st.Ready && spec.Ready.ModelPath != "" && st.Model == "":
			// Ready but naming no model: it may be mid-transition, and wt
			// cannot tell it serves the target.
			unnamed = true
		case st.Ready && spec.Ready.ModelPath != "" && !SameModel(family, st.Model, model):
			other = st.Model
		case st.Ready:
			return nil
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if other != "" {
		return fmt.Errorf("timed out waiting for %s to be ready to serve %s: %s reports an instance serving %s, not %s", family, model, family, other, model)
	}
	if unnamed {
		return fmt.Errorf("timed out waiting for %s to be ready to serve %s: %s reports an instance ready but names no model", family, model, family)
	}
	return fmt.Errorf("timed out waiting for %s to be ready to serve %s", family, model)
}
