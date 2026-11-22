package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
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

const adminBodyLimit = 1 << 20

// List returns the deployments stored in the database (GET /model/info rows
// whose model_info.db_model is true).
func (c *AdminClient) List(ctx context.Context) ([]DBModel, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/model/info", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, c.statusError("list models", status, body)
	}
	var resp struct {
		Data []struct {
			ModelName string         `json:"model_name"`
			Params    map[string]any `json:"litellm_params"`
			Info      map[string]any `json:"model_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("litellm: list models: bad response: %w", err)
	}
	var out []DBModel
	for _, r := range resp.Data {
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
	return out, nil
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
	})
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if strings.Contains(strings.ToLower(string(body)), "store_model_in_db") {
		return ErrDBModelsOff
	}
	return c.statusError("add model", status, body)
}

// Delete removes one database row (POST /model/delete {"id": ...}). A row
// that is already gone (LiteLLM answers HTTP 400 "not found in db"; a 404 is
// accepted too) is nil; any other status is an error.
func (c *AdminClient) Delete(ctx context.Context, id string) error {
	status, body, err := c.do(ctx, http.MethodPost, "/model/delete", map[string]string{"id": id})
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNotFound ||
		(status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "not found in db")) {
		return nil
	}
	return c.statusError("delete model", status, body)
}

func (c *AdminClient) do(ctx context.Context, method, path string, payload any) (int, []byte, error) {
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
		return 0, nil, c.redact(err)
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
		return 0, nil, c.redact(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, adminBodyLimit))
	if err != nil {
		return 0, nil, c.redact(err)
	}
	return resp.StatusCode, body, nil
}

func (c *AdminClient) statusError(what string, status int, body []byte) error {
	msg := c.redactString(strings.TrimSpace(string(body)))
	if len(msg) > 300 {
		cut := 300
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "..."
	}
	return fmt.Errorf("litellm: %s: HTTP %d: %s", what, status, msg)
}

func (c *AdminClient) redact(err error) error {
	return errors.New(c.redactString("litellm: " + err.Error()))
}

func (c *AdminClient) redactString(s string) string {
	if c.Key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Key, "[redacted]")
}
