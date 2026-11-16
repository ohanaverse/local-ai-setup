package modelserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/candidates"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
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
	// RoutedIDs returns the ids the gateway lists right now; a nil func or an
	// error means LiteLLM could not be asked (the catalog then leaves
	// Row.Routed unset).
	RoutedIDs func(ctx context.Context) (map[string]bool, error)
	// Rank orders the registry's models for a slot (the candidates endpoint).
	Rank func(cfg *config.Config, ix tiers.Index, q candidates.Query) []candidates.Candidate
	// TiersFreshness reports when the tiers file was generated and which of
	// its sources are stale; false when there is no readable file.
	TiersFreshness func() (tiers.Freshness, bool)
	// Spend reads the LiteLLM spend log for [start, end). The database
	// address stays inside the real function and never leaves the host.
	Spend func(ctx context.Context, start, end time.Time) (spend.Result, error)
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
		Routed:    w.wait,
		RoutedIDs: w.ids,
		Rank: func(cfg *config.Config, ix tiers.Index, q candidates.Query) []candidates.Candidate {
			return candidates.Rank(cfg.Models, ix, q)
		},
		TiersFreshness: func() (tiers.Freshness, bool) {
			_, f, ok := tiers.LoadWithFreshness()
			return f, ok
		},
		Spend: realQuerySpend,
	}
}

// realQuerySpend reads the spend log through the DSN litellm.DatabaseURL
// finds. The DSN is used here only; spend.Query's errors name no part of it.
func realQuerySpend(ctx context.Context, start, end time.Time) (spend.Result, error) {
	dsn, err := litellm.DatabaseURL()
	if err != nil {
		return spend.Result{}, err
	}
	return spend.Query(ctx, dsn, start, end)
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
			if err != nil {
				return fmt.Errorf("%s is not listed by the gateway after %s (last check: %w); check that the LiteLLM proxy answers, or run 'wt litellm restart'", id, w.timeout, err)
			}
			return fmt.Errorf("%s is not listed by the gateway after %s; check the LiteLLM routes with 'wt litellm list'", id, w.timeout)
		case <-tick.C:
		}
	}
}

// listed asks the gateway once whether it lists id.
func (w routeWaiter) listed(ctx context.Context, id string) (bool, error) {
	ids, err := w.ids(ctx)
	return ids[id], err
}

// ids asks the gateway once for every id it lists.
func (w routeWaiter) ids(ctx context.Context) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.upstream.JoinPath("v1", "models").String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+w.key)
	resp, err := w.client.Do(req)
	if err != nil {
		// url.Error repeats the request URL, an internal endpoint.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the gateway answered %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("the gateway model list could not be read: %w", err)
	}
	ids := make(map[string]bool, len(body.Data))
	for _, m := range body.Data {
		ids[m.ID] = true
	}
	return ids, nil
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
