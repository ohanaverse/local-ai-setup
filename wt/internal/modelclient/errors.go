package modelclient

import (
	"errors"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// UnreachableError says nothing answered at the model server's address.
type UnreachableError struct{ Addr, Reason string }

func (e *UnreachableError) Error() string {
	if e.Addr == unparsedAddr || e.Addr == HiddenURL {
		// No address to name or to suggest for --listen: the url itself
		// is what to fix, and it is not shown.
		return unbuildableRequest + " (it is not shown) — write it as http://HOST:PORT under [model-server] in config.toml, or in WT_MODEL_URL"
	}
	return fmt.Sprintf("model server at %s is not reachable (%s) — start it with 'wt-model serve --listen %s --token-file PATH', or fix url under [model-server] in config.toml",
		clean(e.Addr), clean(e.Reason), clean(e.Addr))
}

// UnauthorizedError says the server refused the token.
type UnauthorizedError struct{ Addr string }

func (e *UnauthorizedError) Error() string {
	return fmt.Sprintf("model server at %s refused the token (%s) — check token_file under [model-server] in config.toml and that the server was started with the same token",
		clean(e.Addr), modelapi.MsgUnauthorized)
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
	return fmt.Sprintf("model server at %s speaks API major %d, this wt speaks %d — %s", clean(e.Addr), e.Server, e.Client, fix)
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
		return fmt.Sprintf("model server at %s speaks catalog schema %d, this wt needs %d — restart wt-model (it is older than this wt)", clean(e.Addr), e.Server, e.Client)
	}
	return fmt.Sprintf("model server at %s sends catalog schema %d, this wt reads up to %d — upgrade wt", clean(e.Addr), e.Server, e.Client)
}

// NotModelServerError says whatever answers at the address is not a wt-model.
type NotModelServerError struct{ Addr, Why string }

func (e *NotModelServerError) Error() string {
	return fmt.Sprintf("%s is not a wt-model server (%s) — check url under [model-server] in config.toml", clean(e.Addr), clean(e.Why))
}

// UnknownModelError is a resolve 404: the server's message says why.
type UnknownModelError struct{ Message string }

func (e *UnknownModelError) Error() string { return e.Message }

// ResponseTooLargeError says the server's answer was larger than the client's
// read limit, so it was not decoded at all: naming the limit is truer than a
// truncation the decoder would misread as the server not speaking this API.
type ResponseTooLargeError struct {
	Addr  string
	Limit int // the read limit, in bytes (32 MiB today)
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("model server at %s sent an answer larger than %d MiB — this wt reads answers up to that size", clean(e.Addr), e.Limit>>20)
}

// GatewayDownError says the gateway kept answering 503 for the whole wait.
type GatewayDownError struct{ Addr string }

func (e *GatewayDownError) Error() string {
	return fmt.Sprintf("the gateway at %s still answers 503 after 3m — the model server may be restarting LiteLLM; try again shortly", clean(e.Addr))
}

// IsCode reports whether err holds a control error with the given code.
func IsCode(err error, code string) bool {
	var re *modelapi.ResponseError
	return errors.As(err, &re) && re.Code == code
}
