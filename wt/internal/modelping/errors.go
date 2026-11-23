package modelping

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// UnreachableError says nothing answered at the model server's address.
type UnreachableError struct{ Addr, Reason string }

func (e *UnreachableError) Error() string {
	if e.Addr == urlrule.Hidden {
		return (&CredentialsInURLError{}).Error()
	}
	if e.Addr == UnparsedAddr {
		// No address to name or to suggest for --listen: the url itself
		// is what to fix, and it is not shown.
		return UnbuildableRequest + " (it is not shown) — write it as http://HOST:PORT under [model-server] in config.toml, or in WT_MODEL_URL"
	}
	return fmt.Sprintf("model server at %s is not reachable (%s) — start it with 'wt-model serve --listen %s --token-file PATH', or fix url under [model-server] in config.toml",
		Clean(e.Addr), Clean(e.Reason), Clean(e.Addr))
}

// UnauthorizedError says the server refused the token.
type UnauthorizedError struct{ Addr string }

func (e *UnauthorizedError) Error() string {
	return fmt.Sprintf("model server at %s refused the token (%s) — check token_file under [model-server] in config.toml and that the server was started with the same token",
		Clean(e.Addr), modelapi.MsgUnauthorized)
}

// SkewError says the server and this wt speak different API majors.
type SkewError struct {
	Addr           string
	Server, Client int
}

func (e *SkewError) Error() string {
	fix := "upgrade wt"
	if e.Server < e.Client {
		fix = "upgrade wt-model"
	}
	return fmt.Sprintf("model server at %s speaks API major %d, this wt speaks %d — %s", Clean(e.Addr), e.Server, e.Client, fix)
}

// SchemaError says the server's catalog schema is not the one this wt reads:
// a newer one needs a newer wt, an older one is a wt-model that was not
// restarted after wt was updated.
type SchemaError struct {
	Addr           string
	Server, Client int
}

func (e *SchemaError) Error() string {
	if e.Server < e.Client {
		return fmt.Sprintf("model server at %s speaks catalog schema %d, this wt needs %d — restart wt-model (it is older than this wt)", Clean(e.Addr), e.Server, e.Client)
	}
	return fmt.Sprintf("model server at %s sends catalog schema %d, this wt reads up to %d — upgrade wt", Clean(e.Addr), e.Server, e.Client)
}

// NotModelServerError says whatever answers at the address is not a wt-model.
type NotModelServerError struct{ Addr, Why string }

func (e *NotModelServerError) Error() string {
	return fmt.Sprintf("%s is not a wt-model server (%s) — check url under [model-server] in config.toml", Clean(e.Addr), Clean(e.Why))
}

// ResponseTooLargeError says the server's answer was larger than the client's
// read limit, so it was not decoded at all: naming the limit is truer than a
// truncation the decoder would misread as the server not speaking this API.
type ResponseTooLargeError struct {
	Addr  string
	Limit int // the read limit, in bytes (32 MiB today)
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("model server at %s sent an answer larger than %d MiB — this wt reads answers up to that size", Clean(e.Addr), e.Limit>>20)
}

// CredentialsInURLError refuses a model-server url that contains an '@',
// whatever its source (WT_MODEL_URL or url under [model-server]). Its text is
// fixed: it quotes nothing of the url. It is a hard refusal (exit 1), never a
// "no model server" case (NoServer is false for it).
type CredentialsInURLError struct{}

func (*CredentialsInURLError) Error() string {
	return "the model server url contains an @ (anywhere in it, read as a user:password); wt does not use credentials in the url: use a token file (WT_MODEL_TOKEN_FILE or token_file under [model-server]); write a literal @ in a path as %40"
}

// TLSTrustError is a TLS failure between this client and the model server,
// with the one hint that fixes it (#597, decision 14): a certificate this
// machine does not trust, one that does not name the host, one outside its
// dates, or a scheme that does not match what the server speaks. Its text
// is the address and the hint; Err (the TLS stack's own error) is kept for
// errors.As and never printed. No hint holds a token.
type TLSTrustError struct {
	Addr, Hint string
	Err        error
}

func (e *TLSTrustError) Error() string {
	return fmt.Sprintf("model server at %s: %s", Clean(e.Addr), e.Hint)
}

func (e *TLSTrustError) Unwrap() error { return e.Err }
