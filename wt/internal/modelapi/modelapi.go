// Package modelapi holds the wire types shared by the wt-model server and its
// clients. It imports only the standard library so any side can depend on it.
// The contract fixture is docs/contracts/model-api.sample.json.
//
// Compatibility within one APIMajor: a minor version may add optional fields
// to any response and to any request body. A server therefore ignores unknown
// request fields, treats an empty request body as an empty JSON object, and
// refuses a body carrying data after its first JSON value; a client ignores
// unknown response fields.
package modelapi

const (
	// APIMajor is the wire API's major version; a client refuses a server
	// whose Ping reports a different one.
	APIMajor = 1
	// SchemaVersion is the catalog document's schema version.
	SchemaVersion = 1
)

// Ping is the reply to the server's liveness/version probe.
type Ping struct {
	APIMajor int    `json:"api_major"`
	Schema   int    `json:"schema"`
	Server   string `json:"server"`
}

// Price is a model's per-token price, in the unit config.PriceInForce uses.
type Price struct {
	Input  *float64 `json:"input,omitempty"`
	Cache  *float64 `json:"cache,omitempty"`
	Output *float64 `json:"output,omitempty"`
	// TimePriced is true when the model has time-windowed prices, so the
	// figure above is the one in force at the catalog's Generated time.
	TimePriced bool `json:"time_priced,omitempty"`
}

// Row states.
const (
	StateRunning = "running"
	StateLoading = "loading"
	StateStopped = "stopped"
	StateBlocked = "blocked"
	StateUnknown = "unknown"
)

// Row actions: what selecting the row would do.
const (
	ActionLaunch = "launch"
	ActionStart  = "start"
	ActionBlock  = "block"
)

// Row is one model in the catalog.
type Row struct {
	ID string `json:"id"`
	// GatewayID is the id the gateway routes the model by; "" for a native
	// row, which never goes through the gateway.
	GatewayID string `json:"gateway_id"`
	// Native marks a model whose provider authenticates natively.
	Native bool `json:"native,omitempty"`
	// Order is the model's place in the registry (discovered rows follow the
	// registry's, by id); clients list rows in this order.
	Order int `json:"order"`
	// Routed is whether the gateway lists the model; nil when the server
	// could not ask LiteLLM, so a client must not read it as false.
	Routed      *bool    `json:"routed,omitempty"`
	Family      string   `json:"family"`
	ProviderID  string   `json:"provider_id"`
	ModelName   string   `json:"model_name"`
	Location    string   `json:"location"`
	Tags        []string `json:"tags"`
	Registered  bool     `json:"registered"`
	Discovered  bool     `json:"discovered"`
	State       string   `json:"state"`
	Action      string   `json:"action"`
	BlockReason string   `json:"block_reason,omitempty"`
	Tier        string   `json:"tier,omitempty"`
	TierCode    string   `json:"tier_code,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	Aligned     *float64 `json:"aligned,omitempty"`
	// TierSources is how many scores the aligned score was made from.
	TierSources int      `json:"tier_sources,omitempty"`
	Context     int      `json:"context,omitempty"`
	Protocols   []string `json:"protocols"`
	Price       *Price   `json:"price,omitempty"`
	Sessions    int      `json:"sessions"`
}

// Pricing describes the OpenRouter price refresh.
type Pricing struct {
	// Updated is the RFC 3339 time of the newest price stamp; absent when
	// prices were never refreshed.
	Updated string `json:"updated,omitempty"`
}

// Catalog is the model list document.
type Catalog struct {
	Schema    int               `json:"schema"`
	Generated string            `json:"generated"`
	Providers map[string]string `json:"providers"`
	Rows      []Row             `json:"rows"`
	// Pricing is present exactly when some model is priced from OpenRouter.
	Pricing *Pricing `json:"pricing,omitempty"`
}

// OthersAnswer names the other resident models the caller agrees to stop.
type OthersAnswer struct {
	Stop []string `json:"stop"`
}

// StartRequest asks the server to start a model.
type StartRequest struct {
	Model        string        `json:"model"`
	AllowReplace bool          `json:"allow_replace"`
	Others       *OthersAnswer `json:"others,omitempty"`
}

// StopRequest asks the server to stop a model.
type StopRequest struct {
	Model        string `json:"model"`
	ConfirmInUse bool   `json:"confirm_in_use"`
	// Session is the requester's client session id: it is left out of the
	// in-use check and of Problem.Sessions, so a session stopping a model only
	// it uses needs no ConfirmInUse.
	Session string `json:"session,omitempty"`
}

// JobRef names a job the server accepted.
type JobRef struct {
	ID string `json:"id"`
}

// Job states.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// StepStillFinishing begins the step of a stop the provider accepted but has
// not finished (style guide S5): the job succeeds and its step reads this,
// then ": " and the engine's text. It is not an error code.
const StepStillFinishing = "still finishing"

// JobStatus is a job's current state.
type JobStatus struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"`
	Model   string     `json:"model"`
	State   string     `json:"state"`
	Step    string     `json:"step"`
	Created string     `json:"created"`
	Updated string     `json:"updated"`
	Result  *JobResult `json:"result,omitempty"`
}

// JobList is the answer of GET /control/v1/jobs: the queued and running jobs,
// oldest first. Jobs is always present, [] when none is active.
type JobList struct {
	Jobs []JobStatus `json:"jobs"`
}

// JobResult is a finished job's outcome.
type JobResult struct {
	OK    bool     `json:"ok"`
	Error *Problem `json:"error,omitempty"`
}

// Candidate is one ranked registry model, the answer's row of
// GET /control/v1/candidates. Score is absent for an unscored model.
type Candidate struct {
	ID                 string   `json:"id"`
	Provider           string   `json:"provider"`
	Location           string   `json:"location"`
	Tier               string   `json:"tier,omitempty"`
	Confidence         string   `json:"confidence,omitempty"`
	Score              *float64 `json:"score,omitempty"`
	Rated              bool     `json:"rated,omitempty"`
	OpenWeights        bool     `json:"open_weights,omitempty"`
	Context            int      `json:"context,omitempty"`
	OutputPerMillion   *float64 `json:"output_per_million,omitempty"`
	Subscription       *float64 `json:"subscription,omitempty"`
	SubscriptionPeriod string   `json:"subscription_period,omitempty"`
	// Flags is always present, [] when the model has none.
	Flags []string `json:"flags"`
}

// CandidateList is the answer of GET /control/v1/candidates. TiersGenerated
// is the RFC 3339 time of the tiers file, absent when there is none;
// StaleSources and Candidates are always present.
type CandidateList struct {
	TiersGenerated string      `json:"tiers_generated,omitempty"`
	StaleSources   []string    `json:"stale_sources"`
	Candidates     []Candidate `json:"candidates"`
}

// SpendRow is one model's totals in the window.
type SpendRow struct {
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	Spend            float64 `json:"spend"`
}

// SpendReport is the answer of GET /control/v1/spend. Rows is always present;
// Unattributed counts requests logged without a model.
type SpendReport struct {
	Rows         []SpendRow `json:"rows"`
	Unattributed int64      `json:"unattributed"`
}

// Problem codes.
const (
	CodeOccupied         = "occupied"
	CodeOthers           = "others"
	CodeOccupancyUnknown = "occupancy_unknown"
	CodeStillFinishing   = "still_finishing"
	CodeInUse            = "in_use"
	CodeStopped          = "stopped"
	CodeFailed           = "failed"
	CodeCancelled        = "cancelled"
	CodeUnknownModel     = "unknown_model"
	CodeBlocked          = "blocked"
)

// Problem describes why a job could not do what was asked.
type Problem struct {
	Code          string     `json:"code"`
	Message       string     `json:"message"`
	Occupants     []string   `json:"occupants,omitempty"`
	Others        []Resident `json:"others,omitempty"`
	OthersUnknown []string   `json:"others_unknown,omitempty"`
	Sessions      []string   `json:"sessions,omitempty"`
}

// Resident is another model resident on the same backend.
type Resident struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Stoppable bool   `json:"stoppable"`
	// Figure is the memory the resident holds, as wt words it.
	Figure string `json:"figure,omitempty"`
}

// SessionRegister announces a session and the models it uses.
type SessionRegister struct {
	ID     string   `json:"id"`
	Models []string `json:"models"`
}

// SessionBeat lists the models to add to a live session's set on a heartbeat;
// removing a model is a re-register (POST sessions).
type SessionBeat struct {
	Models []string `json:"models"`
}

// Control and gateway error codes beyond the job Problem codes above. A
// client may switch on any of them (rule J7), so each is a constant.
const (
	CodeBadRequest       = "bad_request"
	CodeUnauthorized     = "unauthorized"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeUnknownJob       = "unknown_job"
	CodeUnknownSession   = "unknown_session"
	CodeProxyUnavailable = "proxy_unavailable"
	// CodeSpendNotConfigured: nothing names the LiteLLM database, so no spend
	// can be read; CodeSpendUnavailable: the database is named but could not
	// be queried. Both answer 503, and neither message carries the connection
	// string.
	CodeSpendNotConfigured = "spend_not_configured"
	CodeSpendUnavailable   = "spend_unavailable"
)

// Shared error wording and the OpenAI-style error types.
const (
	// MsgUnauthorized is the one wording of a 401, control API and gateway.
	MsgUnauthorized = "missing or wrong bearer token"
	// MsgNotFound and MsgMethodNotAllowed word the control API's routing errors.
	MsgNotFound         = "no such control endpoint"
	MsgMethodNotAllowed = "method not allowed for this control endpoint"
	// MsgProxyUnavailable is the gateway's 503 wording.
	MsgProxyUnavailable = "model proxy is restarting"
	// MsgPathNotServed is the gateway's 404 wording for a path off its
	// allowlist.
	MsgPathNotServed = "path is not served by the gateway"
	// MsgSpendNotConfigured is the spend endpoint's fixed 503 wording when
	// nothing names the LiteLLM database (or this server cannot read spend):
	// fixed text, never a wrapped error, which can name host paths.
	MsgSpendNotConfigured = "spend is not configured on the model server"

	TypeAuthentication = "authentication_error"
	TypeAPI            = "api_error"
	TypeInvalidRequest = "invalid_request_error"
)

// ErrorObject is the object inside every non-2xx control response.
type ErrorObject struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// APIError is the body of every non-2xx control response:
// {"error": {"message": "...", "code": "..."}}.
type APIError struct {
	Error ErrorObject `json:"error"`
}

// Err makes the body usable as a Go error for a client.
func (e APIError) Err() error { return &ResponseError{Message: e.Error.Message, Code: e.Error.Code} }

// ResponseError is a decoded control error as a Go error.
type ResponseError struct {
	Message string
	Code    string
}

func (e *ResponseError) Error() string { return e.Message }

// GatewayErrorObject is the object inside a gateway-authored /v1 error.
type GatewayErrorObject struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// GatewayError is the OpenAI-style body of an error the gateway itself
// writes: {"error": {"message": "...", "type": "...", "code": "..."}}.
type GatewayError struct {
	Error GatewayErrorObject `json:"error"`
}

// ErrorCodes lists every error code a handler may write (job Problem codes
// included); a test keeps the handlers inside it.
var ErrorCodes = []string{
	CodeOccupied, CodeOthers, CodeOccupancyUnknown, CodeStillFinishing, CodeInUse,
	CodeStopped, CodeFailed, CodeCancelled, CodeUnknownModel, CodeBlocked,
	CodeBadRequest, CodeUnauthorized, CodeNotFound, CodeMethodNotAllowed,
	CodeUnknownJob, CodeUnknownSession, CodeProxyUnavailable,
	CodeSpendNotConfigured, CodeSpendUnavailable,
}
