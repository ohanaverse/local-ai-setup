package modelping

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// TestClassifyMapsEachPingError pins the classification table that replaces
// modelsetup's observe switch: every typed ping error gets its Kind (through
// wraps), an unknown type reads as unreachable, nil reads as the zero
// Outcome, and Message is the error's text whole for every non-nil error.
func TestClassifyMapsEachPingError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind Kind
		api  int
		sch  int
	}{
		{"nil", nil, KindNone, 0, 0},
		{"unreachable", &UnreachableError{"a", "refused"}, KindUnreachable, 0, 0},
		{"unauthorized", &UnauthorizedError{"a"}, KindUnauthorized, 0, 0},
		{"not a wt-model", &NotModelServerError{"a", "x"}, KindNotWtModel, 0, 0},
		{"skew", &SkewError{"a", 2, 1}, KindSkew, 2, 0},
		{"schema", &SchemaError{"a", 2, 1}, KindSchema, 0, 2},
		{"wrapped", fmt.Errorf("ctx: %w", &UnauthorizedError{"a"}), KindUnauthorized, 0, 0},
		{"unknown type", errors.New("x"), KindUnreachable, 0, 0},
		{"credentials in url", &CredentialsInURLError{}, KindUnreachable, 0, 0},
	}
	for _, c := range cases {
		got := Classify(c.err)
		if got.Kind != c.kind || got.API != c.api || got.Schema != c.sch {
			t.Errorf("%s: Outcome = %+v, want Kind=%d API=%d Schema=%d", c.name, got, c.kind, c.api, c.sch)
		}
		if c.err == nil {
			if got.Message != "" {
				t.Errorf("nil: Message = %q, want \"\"", got.Message)
			}
			continue
		}
		if got.Message != c.err.Error() {
			t.Errorf("%s: Message = %q, want %q", c.name, got.Message, c.err.Error())
		}
	}
}

// TestCheckedReturnsTheClassifiedFailure pins Checked's triple: a good server
// returns (ping, KindNone, "", nil); a failed ping returns the error beside
// its classification, with Message equal to the error's own text.
func TestCheckedReturnsTheClassifiedFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"api_major":%d,"schema":%d,"server":%q}`, modelapi.APIMajor, modelapi.SchemaVersion, ServerName)))
	}))
	ep := Endpoint{URL: srv.URL, Token: "t"}
	p, kind, msg, err := Checked(context.Background(), ep)
	if err != nil || kind != KindNone || msg != "" {
		t.Errorf("good server: (%v, %d, %q, %v), want (ping, KindNone, \"\", nil)", p, kind, msg, err)
	}
	srv.Close()

	_, kind, msg, err = Checked(context.Background(), ep)
	if err == nil || kind != KindUnreachable {
		t.Fatalf("closed server: (kind %d, msg %q, err %v), want KindUnreachable with an error", kind, msg, err)
	}
	if msg != err.Error() {
		t.Errorf("msg = %q, want the error's own text %q", msg, err.Error())
	}
}
