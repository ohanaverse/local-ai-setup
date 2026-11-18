package modelclient

import (
	"errors"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// The ping errors live below this package in internal/modelping; the names
// this file declares for them are aliases, so a caller's
// errors.As(err, &*modelclient.UnreachableError) keeps matching the type the
// ping actually raises.

// UnreachableError says nothing answered at the model server's address.
type UnreachableError = modelping.UnreachableError

// UnauthorizedError says the server refused the token.
type UnauthorizedError = modelping.UnauthorizedError

// SkewError says the server and this wt speak different API majors.
type SkewError = modelping.SkewError

// SchemaError says the server's catalog schema is not the one this wt reads:
// a newer one needs a newer wt, an older one is a wt-model that was not
// restarted after wt was updated.
type SchemaError = modelping.SchemaError

// NotModelServerError says whatever answers at the address is not a wt-model.
type NotModelServerError = modelping.NotModelServerError

// UnknownModelError is a resolve 404: the server's message says why.
type UnknownModelError struct{ Message string }

func (e *UnknownModelError) Error() string { return e.Message }

// ResponseTooLargeError says the server's answer was larger than the client's
// read limit, so it was not decoded at all: naming the limit is truer than a
// truncation the decoder would misread as the server not speaking this API.
type ResponseTooLargeError = modelping.ResponseTooLargeError

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
