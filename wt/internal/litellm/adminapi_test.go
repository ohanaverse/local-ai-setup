package litellm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

const testMasterKey = "sk-test-master"

// Error bodies below were captured from a real LiteLLM 1.109.1 (scratch
// proxy, scratch database); the fake server answers with the same shapes.
const (
	realStoreOffBody  = `{"error":{"message":"{'error': \"Set ` + "`'STORE_MODEL_IN_DB='True'`" + ` in your env to enable this feature.\"}","type":"internal_server_error","param":null,"code":"500"}}`
	realDuplicateBody = `{"error":{"message":"{'error': 'Failed to add model to db. Check your server logs for more details.'}","type":"internal_server_error","param":null,"code":"500"}}`
	realMissingBody   = `{"error":{"message":"{'error': 'Model with id=wt:ollama/x not found in db'}","type":"invalid_request_error","param":null,"code":"400"}}`
)

// TestAddSendsModelNameParamsAndInfoID pins the /model/new request: the
// caller-chosen model_info.id (what makes a row addressable later), the
// params, and the master key as a bearer token. A wrong body would create
// rows wt cannot find again.
func TestAddSendsModelNameParamsAndInfoID(t *testing.T) {
	var gotAuth, gotPath string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Write([]byte(`{"model_id":"wt:ollama/x"}`))
	}))
	defer srv.Close()
	c := &AdminClient{BaseURL: srv.URL, Key: testMasterKey}
	err := c.Add(context.Background(), DBModel{ID: "wt:ollama/x", ModelName: "alias",
		Params: map[string]any{"model": "ollama/x"}, Info: map[string]any{"wt_managed": true}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/model/new" || gotAuth != "Bearer "+testMasterKey {
		t.Fatalf("path %q auth %q", gotPath, gotAuth)
	}
	if got["model_name"] != "alias" {
		t.Fatalf("body %v", got)
	}
	if p, _ := got["litellm_params"].(map[string]any); p["model"] != "ollama/x" {
		t.Fatalf("params %v", got["litellm_params"])
	}
	info, _ := got["model_info"].(map[string]any)
	if info["id"] != "wt:ollama/x" || info["wt_managed"] != true {
		t.Fatalf("model_info %v", info)
	}
}

// TestAddMapsStoreOffToErrDBModelsOff checks that LiteLLM's real answer when
// store_model_in_db is off (HTTP 500 naming STORE_MODEL_IN_DB in upper case)
// becomes the sentinel callers act on, and that a duplicate id (also a 500,
// with different text) does not.
func TestAddMapsStoreOffToErrDBModelsOff(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		off        bool
	}{
		{"real store-off text", realStoreOffBody, true},
		{"lowercase setting name", `{"error":"store_model_in_db is false"}`, true},
		{"real duplicate text", realDuplicateBody, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			w.Write([]byte(tc.body))
		}))
		err := (&AdminClient{BaseURL: srv.URL, Key: testMasterKey}).Add(context.Background(), DBModel{ID: "i", ModelName: "m"})
		srv.Close()
		if err == nil || errors.Is(err, ErrDBModelsOff) != tc.off {
			t.Errorf("%s: err %v, want off=%v", tc.name, err, tc.off)
		}
	}
}

// TestDeleteMissingIsNil pins delete's idempotence against the real shapes:
// LiteLLM answers an unknown id with HTTP 400 "not found in db" (a plain 404
// is accepted too), and a removal that is already done must not fail a sync.
// It also pins the request body: {"id": ...}.
func TestDeleteMissingIsNil(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{400, realMissingBody}, {404, `{}`}} {
		var got map[string]string
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		err := (&AdminClient{BaseURL: srv.URL, Key: testMasterKey}).Delete(context.Background(), "wt:ollama/x")
		srv.Close()
		if err != nil || path != "/model/delete" || got["id"] != "wt:ollama/x" {
			t.Errorf("status %d: err %v path %q body %v", tc.status, err, path, got)
		}
	}
	// A real failure is still an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	if err := (&AdminClient{BaseURL: srv.URL, Key: testMasterKey}).Delete(context.Background(), "x"); err == nil {
		t.Error("500 must be an error")
	}
}

// TestListKeepsOnlyDatabaseRows checks that List returns only rows LiteLLM
// reports with model_info.db_model true; config.yaml rows (db_model false)
// are not wt's to reconcile through the API.
func TestListKeepsOnlyDatabaseRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" || r.Header.Get("Authorization") != "Bearer "+testMasterKey {
			t.Errorf("path %q auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"data":[
		 {"model_name":"static","litellm_params":{"model":"openai/a"},"model_info":{"id":"h1","db_model":false}},
		 {"model_name":"alias","litellm_params":{"model":"ollama/x"},"model_info":{"id":"wt:ollama/x","db_model":true}}]}`))
	}))
	defer srv.Close()
	rows, err := (&AdminClient{BaseURL: srv.URL, Key: testMasterKey}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "wt:ollama/x" || rows[0].ModelName != "alias" || rows[0].Params["model"] != "ollama/x" {
		t.Fatalf("rows %+v", rows)
	}
}

// TestErrorsNeverContainTheKey checks that a 401 whose body echoes the key
// (a proxy can) never leaks it through any method's error text, since these
// errors reach terminals and logs.
func TestErrorsNeverContainTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"bad key ` + testMasterKey + ` (Bearer ` + testMasterKey + `)"}`))
	}))
	defer srv.Close()
	c := &AdminClient{BaseURL: srv.URL, Key: testMasterKey}
	ctx := context.Background()
	_, e1 := c.List(ctx)
	e2 := c.Add(ctx, DBModel{ID: "i", ModelName: "m"})
	e3 := c.Delete(ctx, "i")
	for _, err := range []error{e1, e2, e3} {
		if err == nil || strings.Contains(err.Error(), testMasterKey) {
			t.Errorf("error %v must exist and omit the key", err)
		}
	}
}

// TestDeleteNotFoundTextOnOtherStatusIsAnError checks that only 404 and the
// verified 400 "not found in db" count as already deleted: a 401 or 500 whose
// body merely says "not found" (a bad key, a gateway page) must surface.
func TestDeleteNotFoundTextOnOtherStatusIsAnError(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte("route not found"))
		}))
		err := (&AdminClient{BaseURL: srv.URL, Key: testMasterKey}).Delete(context.Background(), "x")
		srv.Close()
		if err == nil {
			t.Errorf("status %d with a not-found body must be an error", status)
		}
	}
}

// TestErrorBodyRedactsKeyBeforeTruncating checks that a key straddling the
// 300-byte cut does not survive as a partial prefix, and that the cut never
// splits a multi-byte rune.
func TestErrorBodyRedactsKeyBeforeTruncating(t *testing.T) {
	body := strings.Repeat("a", 295) + testMasterKey + strings.Repeat("é", 50)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	err := (&AdminClient{BaseURL: srv.URL, Key: testMasterKey}).Delete(context.Background(), "x")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "sk-") || strings.Contains(err.Error(), "sk") && strings.Contains(err.Error(), "test-m") {
		t.Errorf("key fragment leaked: %q", err.Error())
	}
	if !utf8.ValidString(err.Error()) {
		t.Errorf("error is not valid UTF-8: %q", err.Error())
	}
}

// TestAdminErrorsNeverQuoteURLUserinfo pins that an admin request's error
// never prints a piece of a url holding an '@' (#640's rule, #598 review):
// Go's transport and parse errors quote the request url whole, password
// included. Both a url that parses and fails to connect and one that does not
// parse are checked; the error names urlrule.Hidden instead.
func TestAdminErrorsNeverQuoteURLUserinfo(t *testing.T) {
	for _, base := range []string{"http://usr:s3cretpw@127.0.0.1:1", "http://usr:s3cretpw@bad host"} {
		c := &AdminClient{BaseURL: base, Key: "sk-key"}
		_, err := c.List(context.Background())
		if err == nil {
			t.Fatalf("%s: List succeeded", base)
		}
		if msg := err.Error(); strings.Contains(msg, "s3cretpw") || strings.Contains(msg, "usr") || !strings.Contains(msg, urlrule.Hidden) {
			t.Fatalf("%s: error %q quotes the url or lacks the placeholder", base, msg)
		}
	}
}
