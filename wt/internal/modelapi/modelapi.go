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
	ID          string   `json:"id"`
	GatewayID   string   `json:"gateway_id"`
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
	Context     int      `json:"context,omitempty"`
	Protocols   []string `json:"protocols"`
	Price       *Price   `json:"price,omitempty"`
	Sessions    int      `json:"sessions"`
}

// Catalog is the model list document.
type Catalog struct {
	Schema    int               `json:"schema"`
	Generated string            `json:"generated"`
	Providers map[string]string `json:"providers"`
	Rows      []Row             `json:"rows"`
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

// JobResult is a finished job's outcome.
type JobResult struct {
	OK    bool     `json:"ok"`
	Error *Problem `json:"error,omitempty"`
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
)

// Shared error wording and the OpenAI-style error types.
const (
	// MsgUnauthorized is the one wording of a 401, control API and gateway.
	MsgUnauthorized = "missing or wrong bearer token"
	// MsgProxyUnavailable is the gateway's 503 wording.
	MsgProxyUnavailable = "model proxy is restarting"

	TypeAuthentication = "authentication_error"
	TypeAPI            = "api_error"
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
}
