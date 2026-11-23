package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// ErrDBModelsOff reports that the proxy refused a database write because
// general_settings.store_model_in_db is off.
var ErrDBModelsOff = errors.New("litellm: database-stored models are off (general_settings.store_model_in_db)")

// DBModel is one database-stored LiteLLM deployment.
type DBModel struct {
	ID        string // model_info.id, chosen by the caller
	ModelName string
	Params    map[string]any // litellm_params
	Info      map[string]any // model_info apart from id
}

// AdminClient calls the proxy's model-management API with the master key.
type AdminClient struct {
	BaseURL, Key string
	HTTP         *http.Client // nil: a client with a 10 s timeout
}

// AdminFromPair is the admin client for the proxy pair (the url and master
// key `wt-model litellm set` stores, which the model server's gateway
// forwards with), or nil when either half is empty: Options.Admin nil is the
// config.yaml path for every row, as before #598.
func AdminFromPair(url, key string) *AdminClient {
	url = strings.TrimRight(url, "/")
	if url == "" || key == "" {
		return nil
	}
	return &AdminClient{BaseURL: url, Key: key}
}

// AdminFor is AdminFromPair over the pair cfg's load resolved
// (config.Config.LitellmBaseURL and LitellmAPIKey).
func AdminFor(cfg *config.Config) *AdminClient {
	if cfg == nil {
		return nil
	}
	return AdminFromPair(cfg.LitellmBaseURL(), cfg.LitellmAPIKey())
}

// adminBodyLimit caps the body of an Add or Delete answer and of any error
// answer; listBodyLimit caps a successful /model/info, which lists every
// deployment (each config.yaml cloud row with LiteLLM's full cost-map
// model_info, a few KB a row), so a large registry passes 1 MiB easily.
const (
	adminBodyLimit = 1 << 20
	listBodyLimit  = 32 << 20
)

// errBodyTooLarge is a successful answer whose body passed the limit: an
// explicit error, never a truncated body that fails to decode.
type errBodyTooLarge struct{ limit int64 }

func (e errBodyTooLarge) Error() string {
	return fmt.Sprintf("response larger than %d bytes", e.limit)
}

// List returns the deployments stored in the database (GET /model/info rows
// whose model_info.db_model is true).
func (c *AdminClient) List(ctx context.Context) ([]DBModel, error) {
	rows, err := c.listRows(ctx)
	if err != nil {
		return nil, err
	}
	return dbModelsOf(rows), nil
}

// modelInfoRow is one /model/info row as listed.
type modelInfoRow struct {
	ModelName string         `json:"model_name"`
	Params    map[string]any `json:"litellm_params"`
	Info      map[string]any `json:"model_info"`
}

// listRows is one GET /model/info: every deployment the proxy serves, any
// owner's. List and ModelInfo read it, and a write that needs both (a sync's
// database half and its subscription check) reads it once (dbRoutes.list).
func (c *AdminClient) listRows(ctx context.Context) ([]modelInfoRow, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/model/info", nil, listBodyLimit)
	if err != nil {
		var big errBodyTooLarge
		if errors.As(err, &big) {
			return nil, fmt.Errorf("litellm: list models: %w", err)
		}
		return nil, err
	}
	if status != http.StatusOK {
		return nil, c.statusError("list models", status, body)
	}
	var resp struct {
		Data []modelInfoRow `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("litellm: list models: bad response: %w", err)
	}
	return resp.Data, nil
}

// dbModelsOf is the database rows of a listing (model_info.db_model true).
func dbModelsOf(rows []modelInfoRow) []DBModel {
	var out []DBModel
	for _, r := range rows {
		if db, _ := r.Info["db_model"].(bool); !db {
			continue
		}
		id, _ := r.Info["id"].(string)
		info := make(map[string]any, len(r.Info))
		for k, v := range r.Info {
			if k != "id" {
				info[k] = v
			}
		}
		out = append(out, DBModel{ID: id, ModelName: r.ModelName, Params: r.Params, Info: info})
	}
	return out
}

// Routes lists the route names (model_name) of the database rows wt owns:
// marked wt_managed and under a DBIDPrefix id, the rows a sync plans with.
// Sorted, each name once. Rows of any other owner are left out.
func (c *AdminClient) Routes(ctx context.Context) ([]string, error) {
	rows, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range rows {
		if isManagedDB(m) && strings.HasPrefix(m.ID, DBIDPrefix) && !slices.Contains(names, m.ModelName) {
			names = append(names, m.ModelName)
		}
	}
	slices.Sort(names)
	return names, nil
}

// ListedRoute is one route `wt-model litellm list` shows from the database:
// a model route (Kind "model", one per name) or one alias deployment (Kind
// "alias", one per row: Name the alias id, Target the registry model id and
// Order the row's litellm_params.order). Never a param.
type ListedRoute struct {
	Name, Kind, Target string
	Order              int
}

// RouteList lists, from one listing, the routes wt owns in the database: the
// model routes Routes names (Kind "model"), then every alias row (IsAliasDB;
// alias and target read from the row id, #596) sorted by alias, order and
// target. Rows of any other owner are left out.
func (c *AdminClient) RouteList(ctx context.Context) ([]ListedRoute, error) {
	rows, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	var models, aliases []ListedRoute
	for _, m := range rows {
		switch {
		case isManagedDB(m) && strings.HasPrefix(m.ID, DBIDPrefix):
			if !slices.ContainsFunc(models, func(r ListedRoute) bool { return r.Name == m.ModelName }) {
				models = append(models, ListedRoute{Name: m.ModelName, Kind: "model"})
			}
		case IsAliasDB(m):
			if alias, target, ok := parseAliasID(m.ID); ok {
				aliases = append(aliases, ListedRoute{Name: alias, Kind: "alias", Target: target, Order: paramOrder(m.Params["order"])})
			}
		}
	}
	slices.SortFunc(models, func(a, b ListedRoute) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(aliases, func(a, b ListedRoute) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		if a.Order != b.Order {
			return a.Order - b.Order
		}
		return strings.Compare(a.Target, b.Target)
	})
	return append(models, aliases...), nil
}

// Add creates one database row (POST /model/new). LiteLLM honors the given
// model_info.id (checked on 1.109.1) and answers a duplicate id with an HTTP
// 500 "Failed to add model to db", so Add is not idempotent.
func (c *AdminClient) Add(ctx context.Context, m DBModel) error {
	info := make(map[string]any, len(m.Info)+1)
	for k, v := range m.Info {
		info[k] = v
	}
	info["id"] = m.ID
	status, body, err := c.do(ctx, http.MethodPost, "/model/new", map[string]any{
		"model_name": m.ModelName, "litellm_params": m.Params, "model_info": info,
	}, adminBodyLimit)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if strings.Contains(strings.ToLower(string(body)), "store_model_in_db") {
		return ErrDBModelsOff
	}
	return c.statusError("add model", status, body, rowSecrets(m)...)
}

// rowSecrets is the spellings of m's resolved api_key (one that is not an
// os.environ/ reference, e.g. rec's key) an error body may echo: as sent,
// and JSON-encoded with and without HTML escaping. nil when there is none.
func rowSecrets(m DBModel) []string {
	v, _ := m.Params["api_key"].(string)
	if v == "" || strings.HasPrefix(v, "os.environ/") {
		return nil
	}
	unquote := func(b []byte) string { return string(b[1 : len(b)-1]) } // a JSON string's quotes
	out := []string{v}
	if b, err := json.Marshal(v); err == nil {
		out = append(out, unquote(b))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) == nil {
		out = append(out, unquote(bytes.TrimSpace(buf.Bytes())))
	}
	return out
}

// Delete removes one database row (POST /model/delete {"id": ...}). A row
// that is already gone (LiteLLM answers HTTP 400 "not found in db"; a 404 is
// accepted too) is nil; any other status is an error.
func (c *AdminClient) Delete(ctx context.Context, id string) error {
	status, body, err := c.do(ctx, http.MethodPost, "/model/delete", map[string]string{"id": id}, adminBodyLimit)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNotFound ||
		(status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "not found in db")) {
		return nil
	}
	return c.statusError("delete model", status, body)
}

// do sends one request and reads at most limit bytes of a 2xx answer (more
// is errBodyTooLarge) and at most adminBodyLimit of any other answer (cut
// there: an error body is only ever quoted, shortened).
func (c *AdminClient) do(ctx context.Context, method, path string, payload any, limit int64) (int, []byte, error) {
	var rd io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return 0, nil, c.redact(ctx, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, c.redact(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		limit = adminBodyLimit
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, nil, c.redact(ctx, err)
	}
	if int64(len(body)) > limit {
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return resp.StatusCode, body[:limit], nil
		}
		return resp.StatusCode, nil, errBodyTooLarge{limit: limit}
	}
	return resp.StatusCode, body, nil
}

// statusError quotes an error answer, shortened, with the master key and
// each of secrets (a row's resolved api_key, rowSecrets) redacted first.
func (c *AdminClient) statusError(what string, status int, body []byte, secrets ...string) error {
	msg := strings.TrimSpace(string(body))
	// Longest first, so a spelling that contains another is redacted whole.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	msg = c.redactString(msg)
	if len(msg) > 300 {
		cut := 300
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "..."
	}
	return fmt.Errorf("litellm: %s: HTTP %d: %s", what, status, msg)
}

// redact turns a request that got no HTTP answer into a *TransportError
// whose text carries no key.
//
// A url holding an '@' (urlrule: read as carrying a user:password) is never
// quoted, in whole or in part: net/url's and the transport's errors quote the
// request url (*url.Error), so its url is replaced by urlrule.Hidden and only
// the cause is kept; an error that quotes it some other way is dropped for a
// fixed text.
func (c *AdminClient) redact(ctx context.Context, err error) error {
	msg := "litellm: " + err.Error()
	if urlrule.HasUserinfo(c.BaseURL) {
		var ue *neturl.Error
		switch {
		case errors.As(err, &ue):
			msg = "litellm: " + ue.Op + " " + urlrule.Hidden + ": " + ue.Err.Error()
		default:
			msg = "litellm: request to " + urlrule.Hidden + " failed"
		}
		if urlrule.HasUserinfo(strings.ReplaceAll(msg, urlrule.Hidden, "")) {
			msg = "litellm: request to " + urlrule.Hidden + " failed"
		}
	}
	return &TransportError{msg: c.redactString(msg), ctxErr: ctx.Err()}
}

// TransportError is an admin request that got no HTTP answer (connection
// refused or reset, a timeout, a cancelled context). Its text is redacted;
// it unwraps to the context's error when the context had ended, so
// errors.Is(err, context.DeadlineExceeded) still works.
type TransportError struct {
	msg    string
	ctxErr error
}

func (e *TransportError) Error() string { return e.msg }
func (e *TransportError) Unwrap() error { return e.ctxErr }

// isTransport reports whether err (or a part of it) is a *TransportError.
func isTransport(err error) bool {
	var t *TransportError
	return errors.As(err, &t)
}

func (c *AdminClient) redactString(s string) string {
	if c.Key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Key, "[redacted]")
}
