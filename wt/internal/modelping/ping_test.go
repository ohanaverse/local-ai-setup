package modelping

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// pingBody renders a ping answer at the given api major, schema and server.
func pingBody(api, schema int, server string) []byte {
	return []byte(fmt.Sprintf(`{"api_major":%d,"schema":%d,"server":%q}`, api, schema, server))
}

// TestCheckPingClassifiesEachAnswer pins the ping classification table: every
// shape of answer maps to its typed error (or to a clean Ping), with the Why
// words callers show. No request is made here: CheckPing is a pure function
// of the response.
func TestCheckPingClassifiesEachAnswer(t *testing.T) {
	good := &Response{Status: http.StatusOK, Body: pingBody(modelapi.APIMajor, modelapi.SchemaVersion, ServerName)}
	if p, err := CheckPing("h:1", good); err != nil || p != (modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: ServerName}) {
		t.Errorf("good ping: (%v, %v), want a clean ping and nil", p, err)
	}

	unauthorized := &Response{Status: http.StatusUnauthorized}
	if _, err := CheckPing("h:1", unauthorized); !isUnauthorized(err) {
		t.Errorf("401: err = %v, want *UnauthorizedError", err)
	}

	for _, status := range []int{http.StatusNotFound, http.StatusTemporaryRedirect} {
		_, err := CheckPing("h:1", &Response{Status: status})
		var ns *NotModelServerError
		if !errors.As(err, &ns) || ns.Why != fmt.Sprintf("GET /control/v1/ping answered HTTP %d", status) {
			t.Errorf("%d: err = %v, want NotModelServerError with the GET/ping Why", status, err)
		}
	}

	notJSON := &Response{Status: http.StatusOK, Body: []byte("nope")}
	if _, err := CheckPing("h:1", notJSON); !whyIs(err, "its ping answer is not JSON") {
		t.Errorf("unparsable ping: err = %v", err)
	}

	other := &Response{Status: http.StatusOK, Body: pingBody(modelapi.APIMajor, modelapi.SchemaVersion, "other")}
	if _, err := CheckPing("h:1", other); !whyIs(err, `it identifies as "other"`) {
		t.Errorf("wrong server name: err = %v", err)
	}

	skewed := &Response{Status: http.StatusOK, Body: pingBody(modelapi.APIMajor+1, modelapi.SchemaVersion, ServerName)}
	p, err := CheckPing("h:1", skewed)
	var sk *SkewError
	if !errors.As(err, &sk) || sk.Addr != "h:1" || sk.Server != modelapi.APIMajor+1 || sk.Client != modelapi.APIMajor {
		t.Fatalf("skew: err = %v, want *SkewError{h:1, %d, %d}", err, modelapi.APIMajor+1, modelapi.APIMajor)
	}
	if p.APIMajor != modelapi.APIMajor+1 {
		t.Errorf("skew: returned Ping.APIMajor = %d, want %d", p.APIMajor, modelapi.APIMajor+1)
	}

	unschema := &Response{Status: http.StatusOK, Body: pingBody(modelapi.APIMajor, modelapi.SchemaVersion+1, ServerName)}
	p, err = CheckPing("h:1", unschema)
	var sc *SchemaError
	if !errors.As(err, &sc) || sc.Server != modelapi.SchemaVersion+1 || sc.Client != modelapi.SchemaVersion {
		t.Fatalf("schema: err = %v, want *SchemaError{h:1, %d, %d}", err, modelapi.SchemaVersion+1, modelapi.SchemaVersion)
	}
	if p.Schema != modelapi.SchemaVersion+1 {
		t.Errorf("schema: returned Ping.Schema = %d, want %d", p.Schema, modelapi.SchemaVersion+1)
	}
}

// isUnauthorized reports whether err is (or wraps) a *UnauthorizedError.
func isUnauthorized(err error) bool {
	var ua *UnauthorizedError
	return errors.As(err, &ua)
}

// whyIs reports whether err is a *NotModelServerError with exactly this Why.
func whyIs(err error, want string) bool {
	var ns *NotModelServerError
	return errors.As(err, &ns) && ns.Why == want
}

// TestSendRefusesAnAtURLBeforeAnyRequest pins Send's fail-closed first step:
// a url holding an '@' raises *CredentialsInURLError with zero requests made.
func TestSendRefusesAnAtURLBeforeAnyRequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()

	ep := Endpoint{URL: "http://u:p@" + srv.Listener.Addr().String(), Token: "t"}
	_, err := Send(context.Background(), NewHTTPClient(), time.Second, ep, "GET", ep.Origin()+"/x", nil)
	var ci *CredentialsInURLError
	if !errors.As(err, &ci) {
		t.Errorf("err = %v, want *CredentialsInURLError", err)
	}
	if hits != 0 {
		t.Errorf("the server was reached %d times; a refusal happens before any request", hits)
	}
}

// TestSendHostlessURLQuotesNothing pins that a url net/http cannot build a
// request from fails with fixed text that quotes none of it.
func TestSendHostlessURLQuotesNothing(t *testing.T) {
	ep := Endpoint{URL: "hostzz9", Token: "t"}
	_, err := Send(context.Background(), NewHTTPClient(), time.Second, ep, "GET", ep.Origin()+"/x", nil)
	var un *UnreachableError
	if !errors.As(err, &un) || un.Addr != UnparsedAddr || un.Reason != UnbuildableRequest {
		t.Fatalf("err = %v, want *UnreachableError{%q, %q}", err, UnparsedAddr, UnbuildableRequest)
	}
	if strings.Contains(err.Error(), "hostzz9") {
		t.Errorf("the refusal quotes the url: %q", err)
	}
}

// TestSendNamesTheReadLimit pins that an answer one byte past the limit is
// refused by name (*ResponseTooLargeError with the limit) instead of being
// truncated into a decode error.
func TestSendNamesTheReadLimit(t *testing.T) {
	big := bytes.Repeat([]byte("x"), MaxResponse+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	ep := Endpoint{URL: srv.URL, Token: "t"}
	_, err := Send(context.Background(), NewHTTPClient(), time.Minute, ep, "GET", srv.URL+"/x", nil)
	var rl *ResponseTooLargeError
	if !errors.As(err, &rl) || rl.Limit != MaxResponse {
		t.Errorf("err = %v, want *ResponseTooLargeError{Limit: %d}", err, MaxResponse)
	}
}

// TestSendTimeoutSaysTimedOut pins the timeout's reason wording: the bounded
// wait expires and the error says how long it waited.
func TestSendTimeoutSaysTimedOut(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
	}))
	defer func() { close(done); srv.Close() }()

	ep := Endpoint{URL: srv.URL, Token: "t"}
	_, err := Send(context.Background(), NewHTTPClient(), 50*time.Millisecond, ep, "GET", srv.URL, nil)
	var un *UnreachableError
	if !errors.As(err, &un) || !strings.HasPrefix(un.Reason, "timed out after ") {
		t.Errorf("err = %v, want *UnreachableError whose Reason starts %q", err, "timed out after ")
	}
}

// TestSendCancelledContextIsTheContextError pins that a cancelled context is
// the caller's error, verbatim — never disguised as the server being down.
func TestSendCancelledContextIsTheContextError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ep := Endpoint{URL: srv.URL, Token: "t"}
	_, err := Send(ctx, NewHTTPClient(), time.Second, ep, "GET", srv.URL, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	var un *UnreachableError
	if errors.As(err, &un) {
		t.Errorf("a cancelled ctx was reported as unreachable: %v", err)
	}
}

// TestSendSendsTheBearerAndJSONHeaders pins what Send puts on the wire: the
// bearer token and Accept always, Content-Type only when there is a body.
func TestSendSendsTheBearerAndJSONHeaders(t *testing.T) {
	type seen struct{ auth, accept, contentType string }
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, seen{r.Header.Get("Authorization"), r.Header.Get("Accept"), r.Header.Get("Content-Type")})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ep := Endpoint{URL: srv.URL, Token: "t"}
	if _, err := Send(context.Background(), NewHTTPClient(), time.Second, ep, "GET", srv.URL, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), NewHTTPClient(), time.Second, ep, "GET", srv.URL, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	if got[0].auth != "Bearer t" || got[0].accept != "application/json" || got[0].contentType != "application/json" {
		t.Errorf("with a body: headers = %+v", got[0])
	}
	if got[1].contentType != "" {
		t.Errorf("without a body: Content-Type = %q, want none", got[1].contentType)
	}
}

// TestNewHTTPClientIgnoresEnvProxyAndRedirects pins the transport shape: no
// environment proxy may see the bearer token, and a 3xx is never followed
// (it would replay the token to wherever Location names).
func TestNewHTTPClientIgnoresEnvProxyAndRedirects(t *testing.T) {
	hc := NewHTTPClient()
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", hc.Transport)
	}
	if tr.Proxy != nil {
		t.Error("Proxy is set; a environment HTTP(S)_PROXY must never see the token")
	}
	if got := hc.CheckRedirect(nil, nil); got != http.ErrUseLastResponse {
		t.Errorf("CheckRedirect = %v, want http.ErrUseLastResponse", got)
	}
}

// TestPingAgainstAServer pins Ping end to end: a good server yields its
// identity, a closed one yields *UnreachableError worded "is not reachable".
func TestPingAgainstAServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/control/v1/ping" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pingBody(modelapi.APIMajor, modelapi.SchemaVersion, ServerName))
	}))
	defer srv.Close()

	p, err := Ping(context.Background(), Endpoint{URL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatalf("good server: %v", err)
	}
	if p.Server != ServerName {
		t.Errorf("Server = %q, want %q", p.Server, ServerName)
	}

	closed := srv.URL
	srv.Close()
	if _, err := Ping(context.Background(), Endpoint{URL: closed, Token: "t"}); !strings.Contains(fmt.Sprint(err), "is not reachable (") {
		var un *UnreachableError
		if !errors.As(err, &un) {
			t.Errorf("closed server: err = %v, want *UnreachableError", err)
		}
	}
}

// TestGetJSONPingsFirstThenDecodes pins GetJSON's order and its failure
// words: the handshake first (its error as is), then the GET, with 401, a
// non-2xx and an undecodable body each mapped to its typed error.
func TestGetJSONPingsFirstThenDecodes(t *testing.T) {
	t.Run("ping first then decode", func(t *testing.T) {
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			switch r.URL.Path {
			case "/control/v1/ping":
				_, _ = w.Write(pingBody(modelapi.APIMajor, modelapi.SchemaVersion, ServerName))
			case "/x":
				_, _ = w.Write([]byte(`{"a":1}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		var out map[string]any
		if err := GetJSON(context.Background(), Endpoint{URL: srv.URL, Token: "t"}, "/x", &out); err != nil {
			t.Fatalf("GetJSON: %v", err)
		}
		if len(paths) != 2 || paths[0] != "/control/v1/ping" || paths[1] != "/x" {
			t.Errorf("paths = %v, want [/control/v1/ping /x]", paths)
		}
		if out["a"] != 1.0 {
			t.Errorf("out = %v, want a=1", out)
		}
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/v1/ping":
			_, _ = w.Write(pingBody(modelapi.APIMajor, modelapi.SchemaVersion, ServerName))
		case "/x":
			w.WriteHeader(http.StatusUnauthorized)
		case "/y":
			w.WriteHeader(http.StatusInternalServerError)
		case "/z":
			_, _ = w.Write([]byte("{"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ep := Endpoint{URL: srv.URL, Token: "t"}

	if err := GetJSON(context.Background(), ep, "/x", nil); err == nil || !isUnauthorized(err) {
		t.Errorf("401: err = %v, want *UnauthorizedError", err)
	}
	if err := GetJSON(context.Background(), ep, "/y", nil); !whyIs(err, "GET /y answered HTTP 500") {
		t.Errorf("500: err = %v", err)
	}
	if err := GetJSON(context.Background(), ep, "/z", nil); !whyIs(err, "its answer to GET /z is not the expected JSON") {
		t.Errorf("bad JSON: err = %v", err)
	}
}
