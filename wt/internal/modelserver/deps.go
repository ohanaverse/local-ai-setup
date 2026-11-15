package modelserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// StopBatcher finishes one model's stop (lifecycle.StopBatch's Stop).
type StopBatcher interface {
	Stop(ctx context.Context, en localmodels.Entry) (restartOwed bool, err error)
}

// Deps is everything the start/stop jobs and the auto-stopper reach outside
// the process through. DefaultDeps wires the real lifecycle engine, inventory
// probe and gateway; tests build one from closures.
type Deps struct {
	// Config returns the host config. It is called at each request, job and
	// scan, so edits to the registry (wt model, wt cloud-sync) show without a
	// restart; DefaultDeps backs it with a reload that keeps the last good
	// config when the file cannot be read.
	Config       func() *config.Config
	Inventory    func(*config.Config) localmodels.Snapshot
	Tiers        func() tiers.Index
	Now          func() time.Time
	Start        func(context.Context, *config.Config, lifecycle.Target, lifecycle.Options) error
	Settle       func(context.Context, io.Writer, *config.Config, lifecycle.Target) error
	BeginStops   func(context.Context, *config.Config, []localmodels.Entry) StopBatcher
	SettleRoutes func(context.Context, io.Writer, *config.Config)
	// Routed returns nil once gatewayID answers through the gateway.
	Routed func(ctx context.Context, gatewayID string) error
}

// routePollEvery is how often the default Routed asks the gateway.
const routePollEvery = 500 * time.Millisecond

// routeRequestTimeout bounds one /v1/models request of the default Routed.
const routeRequestTimeout = 5 * time.Second

// reloadingConfig returns a Deps.Config getter: each call runs reload and
// returns the fresh config, or the last good one (initially cfg) when reload
// fails, so a half-written registry never fails a request. Safe for
// concurrent use.
func reloadingConfig(cfg *config.Config, reload func() (*config.Config, error)) func() *config.Config {
	var mu sync.Mutex
	last := cfg
	return func() *config.Config {
		mu.Lock()
		defer mu.Unlock()
		if fresh, err := reload(); err == nil && fresh != nil {
			last = fresh
		}
		return last
	}
}

// DefaultDeps wires the real functions: the only place this package names a
// lifecycle function that does I/O. cfg is the initial config and reload
// (production passes config.Load) re-reads it on each use. Routed polls GET <upstream>/v1/models with
// upstreamKey every 500 ms for up to litellm.ProxyReadyTimeout. SettleRoutes
// also waits for the proxy restart it starts, so a stop job ends with the
// route gone.
func DefaultDeps(cfg *config.Config, reload func() (*config.Config, error), upstream *url.URL, upstreamKey string) Deps {
	w := routeWaiter{
		client:   &http.Client{Timeout: routeRequestTimeout},
		upstream: upstream, key: upstreamKey,
		every: routePollEvery, timeout: litellm.ProxyReadyTimeout,
	}
	return Deps{
		Config:    reloadingConfig(cfg, reload),
		Inventory: localmodels.Inventory,
		Tiers:     tiers.Load,
		Now:       time.Now,
		Start:     lifecycle.Start,
		Settle:    lifecycle.SettleStart,
		BeginStops: func(ctx context.Context, cfg *config.Config, entries []localmodels.Entry) StopBatcher {
			return lifecycle.BeginStops(ctx, cfg, entries)
		},
		SettleRoutes: func(ctx context.Context, out io.Writer, cfg *config.Config) {
			lifecycle.SettleRoutesTo(ctx, out, cfg)
			lifecycle.WaitPendingRoutes()
		},
		Routed: w.wait,
	}
}

// routeWaiter is the default Routed: it polls the gateway's model list. Its
// interval and timeout are fields so a test can shorten them.
type routeWaiter struct {
	client   *http.Client
	upstream *url.URL
	key      string
	every    time.Duration
	timeout  time.Duration
}

// wait polls until id is listed, the timeout passes (an error naming id and
// the last problem) or ctx ends (ctx's error).
func (w routeWaiter) wait(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.NewTimer(w.timeout)
	defer deadline.Stop()
	for {
		ok, err := w.listed(ctx, id)
		if ok {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tick := time.NewTimer(w.every)
		select {
		case <-ctx.Done():
			tick.Stop()
			return ctx.Err()
		case <-deadline.C:
			tick.Stop()
			msg := fmt.Sprintf("%s is not listed by the gateway after %s", id, w.timeout)
			if err != nil {
				msg += " (last check: " + err.Error() + ")"
			}
			return fmt.Errorf("%s", msg)
		case <-tick.C:
		}
	}
}

// listed asks the gateway once whether it lists id.
func (w routeWaiter) listed(ctx context.Context, id string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.upstream.JoinPath("v1", "models").String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+w.key)
	resp, err := w.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET /v1/models: %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, fmt.Errorf("GET /v1/models: %w", err)
	}
	for _, m := range body.Data {
		if m.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// syncBuffer is an io.Writer safe to write from the engine's restart
// goroutine while the job reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// withOutput appends what the engine printed to a failure message.
func withOutput(msg string, out *syncBuffer) string {
	if s := strings.TrimSpace(out.String()); s != "" {
		return msg + "\n" + s
	}
	return msg
}

func problem(code, msg string) modelapi.JobResult {
	return modelapi.JobResult{Error: &modelapi.Problem{Code: code, Message: msg}}
}

// guarded turns a panic inside run into a failed result: a lifecycle panic
// must fail its job, not crash the daemon.
func guarded(run modeljobs.RunFunc) modeljobs.RunFunc {
	return func(ctx context.Context, step func(string)) (res modelapi.JobResult) {
		defer func() {
			if r := recover(); r != nil {
				res = problem(modelapi.CodeFailed, fmt.Sprintf("internal error: %v", r))
			}
		}()
		return run(ctx, step)
	}
}
