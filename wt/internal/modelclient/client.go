package modelclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

const (
	defaultRequestTimeout = modelping.DefaultTimeout
	dialTimeout           = modelping.DialTimeout
	endBound              = 3 * time.Second
	gatewayWaitCap        = 3 * time.Minute
	gatewayRetryDefault   = 5 * time.Second
	maxResponse           = modelping.MaxResponse
	serverName            = modelping.ServerName
)

// Client talks to the model server's control API. It is safe for concurrent
// use.
type Client struct {
	ep      Endpoint
	hc      *http.Client
	timeout time.Duration
	sleep   func(time.Duration) // nil: a ctx-aware timer
	gwCap   time.Duration
	drive   driveOptions

	mu     sync.Mutex
	pinged bool
	ping   modelapi.Ping
	offset time.Duration
}

// Option configures New.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client (a 5 s dial timeout and no
// overall timeout: the per-request timeout bounds each control call). A
// caller-supplied client keeps its own redirect policy, so one that follows
// redirects could replay the bearer token to the redirect target.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithRequestTimeout sets the bound on one control call.
func WithRequestTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// withGatewayCap shortens the gateway wait cap for a test.
func withGatewayCap(d time.Duration) Option { return func(c *Client) { c.gwCap = d } }

// WithSleep replaces the sleep WaitGatewayReady and the job driver use
// between polls.
func WithSleep(f func(time.Duration)) Option { return func(c *Client) { c.sleep = f } }

// New returns a client for ep.
func New(ep Endpoint, opts ...Option) *Client {
	c := &Client{ep: ep, timeout: defaultRequestTimeout, gwCap: gatewayWaitCap}
	for _, o := range opts {
		o(c)
	}
	if c.hc == nil {
		c.hc = modelping.NewHTTPClient()
	}
	return c
}

// addr is host:port of the endpoint, as the error texts and the server's
// --listen flag name it: modelping.Addr, which never echoes the raw url (an
// '@' url shows urlrule.Hidden whole, a url with no host shows the fixed
// unparsedAddr).
func (c *Client) addr() string { return modelping.Addr(c.ep.URL) }

// Now is the server's clock: the local clock plus the offset measured from the
// Date header of the latest response (the local clock before the first one).
func (c *Client) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

// response is modelping.Response under this package's name: what a request
// came back with, whatever its status.
type response = modelping.Response

// send is modelping.Send against this client's transport and timeout, plus
// the one client-side effect of a response: the Date offset that makes Now()
// the server's clock.
func (c *Client) send(ctx context.Context, method, rawurl string, body any) (*response, error) {
	r, err := modelping.Send(ctx, c.hc, c.timeout, c.ep, method, rawurl, body)
	if err != nil {
		return nil, err
	}
	if d, perr := http.ParseTime(r.Header.Get("Date")); perr == nil {
		c.mu.Lock()
		c.offset = time.Until(d)
		c.mu.Unlock()
	}
	return r, nil
}

// statusErr turns a non-2xx response into the typed error for it.
func (c *Client) statusErr(r *response) error {
	if r.Status >= 300 && r.Status < 400 {
		return &NotModelServerError{Addr: c.addr(), Why: "it answered HTTP " + strconv.Itoa(r.Status) + ", a redirect, which wt does not follow"}
	}
	if r.Status == http.StatusUnauthorized {
		return &UnauthorizedError{Addr: c.addr()}
	}
	ae, ok := modelping.DecodeAPIError(r.Body)
	if !ok {
		return &modelapi.ResponseError{Message: "model server answered HTTP " + strconv.Itoa(r.Status)}
	}
	return ae.Err()
}

// do is the one control-call helper: the request, the typed error mapping and
// the JSON decode into out (nil for none). Every call but Ping and End first
// checks the server's version.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	r, err := c.exchange(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return &NotModelServerError{Addr: c.addr(), Why: "its answer to " + method + " " + path + " is not the expected JSON"}
	}
	return nil
}

func (c *Client) exchange(ctx context.Context, method, path string, q url.Values, body any) (*response, error) {
	u := c.ep.Origin() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, err := c.send(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if r.Status < 200 || r.Status > 299 {
		return r, c.statusErr(r)
	}
	return r, nil
}

// checked runs do after the version handshake.
func (c *Client) checked(ctx context.Context, method, path string, q url.Values, body, out any) error {
	if _, err := c.Ping(ctx); err != nil {
		return err
	}
	return c.do(ctx, method, path, q, body, out)
}

// Ping checks the server answers, is a wt-model and speaks this API major. It
// asks once per Client; every other method calls it first: the handshake
// itself is modelping.CheckPing over modelping.Send.
func (c *Client) Ping(ctx context.Context) (modelapi.Ping, error) {
	c.mu.Lock()
	if c.pinged {
		p := c.ping
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()
	r, err := c.send(ctx, "GET", c.ep.Origin()+"/control/v1/ping", nil)
	if err != nil {
		return modelapi.Ping{}, err
	}
	p, err := modelping.CheckPing(c.addr(), r)
	if err != nil {
		return p, err
	}
	c.mu.Lock()
	c.pinged, c.ping = true, p
	c.mu.Unlock()
	return p, nil
}

// Catalog reads the model list.
func (c *Client) Catalog(ctx context.Context) (modelapi.Catalog, error) {
	var cat modelapi.Catalog
	if err := c.checked(ctx, "GET", "/control/v1/catalog", nil, nil, &cat); err != nil {
		return cat, err
	}
	if cat.Schema != modelapi.SchemaVersion {
		return modelapi.Catalog{}, &SchemaError{Addr: c.addr(), Server: cat.Schema, Client: modelapi.SchemaVersion}
	}
	cleanCatalog(&cat)
	return cat, nil
}

// Resolve reads one model's row; an id the server does not know is
// *UnknownModelError. The id goes in the query, never in the path.
func (c *Client) Resolve(ctx context.Context, id string) (modelapi.Row, error) {
	var row modelapi.Row
	err := c.checked(ctx, "GET", "/control/v1/resolve", url.Values{"model": {id}}, nil, &row)
	if IsCode(err, modelapi.CodeUnknownModel) {
		return row, &UnknownModelError{Message: err.Error()}
	}
	cleanRow(&row)
	return row, err
}

// CandidateQuery filters Candidates; the zero value asks for everything.
type CandidateQuery struct {
	OpenOnly    bool
	Providers   []string
	Near        string
	PerProvider int
}

// Candidates reads the ranked registry models.
func (c *Client) Candidates(ctx context.Context, q CandidateQuery) (modelapi.CandidateList, error) {
	v := url.Values{}
	if q.OpenOnly {
		v.Set("open_only", "true")
	}
	if len(q.Providers) > 0 {
		v.Set("provider", strings.Join(q.Providers, ","))
	}
	if q.Near != "" {
		v.Set("near", q.Near)
	}
	if q.PerProvider > 0 {
		v.Set("per_provider", strconv.Itoa(q.PerProvider))
	}
	var out modelapi.CandidateList
	if err := c.checked(ctx, "GET", "/control/v1/candidates", v, nil, &out); err != nil {
		return out, err
	}
	out.TiersGenerated = clean(out.TiersGenerated)
	cleanAll(out.StaleSources)
	for i := range out.Candidates {
		cd := &out.Candidates[i]
		cd.ID, cd.Provider, cd.Location = clean(cd.ID), clean(cd.Provider), clean(cd.Location)
		cd.Tier, cd.Confidence, cd.SubscriptionPeriod = clean(cd.Tier), clean(cd.Confidence), clean(cd.SubscriptionPeriod)
		cleanAll(cd.Flags)
	}
	return out, nil
}

// Spend reads the LiteLLM spend log between start and end. Both bounds go
// with their fractional seconds (RFC 3339 allows them; the server parses
// them): `wt stats` buckets its launches against the same instant, and a
// bound cut to the second would leave a request at the report's instant out
// of the spend window while its launch is in.
func (c *Client) Spend(ctx context.Context, start, end time.Time) (modelapi.SpendReport, error) {
	v := url.Values{"start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}}
	var out modelapi.SpendReport
	if err := c.checked(ctx, "GET", "/control/v1/spend", v, nil, &out); err != nil {
		return out, err
	}
	for i := range out.Rows {
		out.Rows[i].Model = clean(out.Rows[i].Model)
	}
	return out, nil
}

// SubmitStart asks the server to start a model and returns the job's id.
func (c *Client) SubmitStart(ctx context.Context, req modelapi.StartRequest) (string, error) {
	var ref modelapi.JobRef
	err := c.checked(ctx, "POST", "/control/v1/start", nil, req, &ref)
	return ref.ID, err
}

// SubmitStop asks the server to stop a model and returns the job's id.
func (c *Client) SubmitStop(ctx context.Context, req modelapi.StopRequest) (string, error) {
	var ref modelapi.JobRef
	err := c.checked(ctx, "POST", "/control/v1/stop", nil, req, &ref)
	return ref.ID, err
}

// Job reads one job's state.
func (c *Client) Job(ctx context.Context, id string) (modelapi.JobStatus, error) {
	var js modelapi.JobStatus
	err := c.checked(ctx, "GET", "/control/v1/jobs/"+url.PathEscape(id), nil, nil, &js)
	cleanJob(&js)
	return js, err
}

// ActiveJobs lists the queued and running jobs, of one model when model is set.
func (c *Client) ActiveJobs(ctx context.Context, model string) ([]modelapi.JobStatus, error) {
	v := url.Values{}
	if model != "" {
		v.Set("model", model)
	}
	var jl modelapi.JobList
	if err := c.checked(ctx, "GET", "/control/v1/jobs", v, nil, &jl); err != nil {
		return nil, err
	}
	for i := range jl.Jobs {
		cleanJob(&jl.Jobs[i])
	}
	return jl.Jobs, nil
}

// CancelJob asks the server to cancel a job.
func (c *Client) CancelJob(ctx context.Context, id string) error {
	return c.checked(ctx, "POST", "/control/v1/jobs/"+url.PathEscape(id)+"/cancel", nil, nil, nil)
}

// Register announces a client session and the models it uses.
func (c *Client) Register(ctx context.Context, id string, models []string) error {
	return c.checked(ctx, "POST", "/control/v1/sessions", nil, modelapi.SessionRegister{ID: id, Models: models}, nil)
}

// Heartbeat renews a client session and adds models to its set. known is false
// when the server no longer knows the session (410): register again.
func (c *Client) Heartbeat(ctx context.Context, id string, models []string) (known bool, err error) {
	if _, err = c.Ping(ctx); err != nil {
		return false, err
	}
	r, err := c.exchange(ctx, "POST", "/control/v1/sessions/"+url.PathEscape(id)+"/heartbeat", nil, modelapi.SessionBeat{Models: models})
	if r != nil && r.Status == http.StatusGone {
		return false, nil
	}
	return err == nil, err
}

// End removes a client session. It is for the exit path, so it runs even when
// ctx is already cancelled, and never takes longer than 3 s. It does not
// handshake first: a session that registered has already.
func (c *Client) End(ctx context.Context, id string) error {
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endBound)
	defer cancel()
	return c.do(ectx, "DELETE", "/control/v1/sessions/"+url.PathEscape(id), nil, nil, nil)
}

// WaitGatewayReady polls the gateway's model list until it answers. A 503
// (LiteLLM restarting) waits its Retry-After (5 s when absent, unparseable or
// not positive), clamped to [1 s, what is left of the cap]. The cap is 3
// minutes of elapsed time, polls included (sleeps as injected by WithSleep,
// polls as measured), then *GatewayDownError. A cancelled ctx ends the wait.
func (c *Client) WaitGatewayReady(ctx context.Context) error {
	if _, err := c.Ping(ctx); err != nil {
		return err
	}
	var slept, polled time.Duration
	for {
		t0 := time.Now()
		r, err := c.send(ctx, "GET", c.ep.Origin()+"/v1/models", nil)
		polled += time.Since(t0)
		if err != nil {
			return err
		}
		switch {
		case r.Status >= 200 && r.Status <= 299:
			return nil
		case r.Status != http.StatusServiceUnavailable:
			return c.statusErr(r)
		}
		if slept+polled >= c.gwCap {
			return &GatewayDownError{Addr: c.addr()}
		}
		d := retryAfter(r.Header.Get("Retry-After"))
		d = min(max(d, time.Second), max(c.gwCap-slept, time.Second))
		if err := c.sleepFor(ctx, d); err != nil {
			return err
		}
		slept += d
	}
}

// GatewayStatus asks the gateway's model list once and returns the HTTP
// status it answered. It waits for nothing and retries nothing.
func (c *Client) GatewayStatus(ctx context.Context) (int, error) {
	r, err := c.send(ctx, "GET", c.ep.Origin()+"/v1/models", nil)
	if err != nil {
		return 0, err
	}
	return r.Status, nil
}

// retryAfter reads a delay-seconds Retry-After; anything else, or a value past
// a day, is the default (bounded before the multiplication, so it cannot wrap).
func retryAfter(v string) time.Duration {
	s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || s <= 0 || s > 24*3600 {
		return gatewayRetryDefault
	}
	return time.Duration(s) * time.Second
}

// sleepFor waits d: through the injected sleep, or a timer that ctx cancels.
func (c *Client) sleepFor(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		c.sleep(d)
		return ctx.Err()
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tm.C:
		return nil
	}
}

func cleanRow(r *modelapi.Row) {
	r.ID, r.GatewayID, r.Family = clean(r.ID), clean(r.GatewayID), clean(r.Family)
	r.ProviderID, r.ModelName, r.Location = clean(r.ProviderID), clean(r.ModelName), clean(r.Location)
	r.State, r.Action, r.BlockReason = clean(r.State), clean(r.Action), clean(r.BlockReason)
	r.Tier, r.TierCode, r.Confidence = clean(r.Tier), clean(r.TierCode), clean(r.Confidence)
	cleanAll(r.Tags)
	cleanAll(r.Protocols)
}

func cleanCatalog(c *modelapi.Catalog) {
	c.Generated = clean(c.Generated)
	if c.Providers != nil {
		ps := make(map[string]string, len(c.Providers))
		for k, v := range c.Providers {
			ps[clean(k)] = clean(v)
		}
		c.Providers = ps
	}
	for i := range c.Rows {
		cleanRow(&c.Rows[i])
	}
	if c.Pricing != nil {
		c.Pricing.Updated = clean(c.Pricing.Updated)
	}
}

func cleanJob(js *modelapi.JobStatus) {
	js.ID, js.Kind, js.Model, js.State = clean(js.ID), clean(js.Kind), clean(js.Model), clean(js.State)
	js.Step, js.Created, js.Updated = clean(js.Step), clean(js.Created), clean(js.Updated)
	if js.Result == nil || js.Result.Error == nil {
		return
	}
	p := js.Result.Error
	p.Code, p.Message = clean(p.Code), clean(p.Message)
	cleanAll(p.Occupants)
	cleanAll(p.OthersUnknown)
	cleanAll(p.Sessions)
	for i := range p.Others {
		o := &p.Others[i]
		o.ID, o.Label, o.Figure = clean(o.ID), clean(o.Label), clean(o.Figure)
	}
}
