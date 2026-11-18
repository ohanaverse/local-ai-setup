package modelping

import (
	"context"
	"errors"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// Kind says how a ping failed: the names and the order modelsetup's observe
// state has used since the model server tab existed.
type Kind int

const (
	KindNone Kind = iota
	KindUnreachable
	KindUnauthorized
	KindNotWtModel
	KindSkew
	KindSchema
)

// Outcome is one classified ping failure: its Kind, the error's text whole
// (Message, empty for a nil error), and the server's api major or schema when
// a skew or schema error says what it speaks.
type Outcome struct {
	Kind    Kind
	Message string
	API     int
	Schema  int
}

// Classify maps a ping error onto an Outcome: nil is the zero Outcome; a
// typed error is its Kind, through any wrap (errors.As); an error of no known
// type reads as unreachable, since a ping only fails at the transport. Its
// Message is err.Error() whole, so a caller that shows the message never has
// to re-derive it from the type.
func Classify(err error) Outcome {
	if err == nil {
		return Outcome{}
	}
	o := Outcome{Message: err.Error()}
	var un *UnreachableError
	var ua *UnauthorizedError
	var ns *NotModelServerError
	var sk *SkewError
	var sc *SchemaError
	switch {
	case errors.As(err, &ua):
		o.Kind = KindUnauthorized
	case errors.As(err, &ns):
		o.Kind = KindNotWtModel
	case errors.As(err, &sk):
		o.Kind, o.API = KindSkew, sk.Server
	case errors.As(err, &sc):
		o.Kind, o.Schema = KindSchema, sc.Server
	case errors.As(err, &un):
		o.Kind = KindUnreachable
	default:
		o.Kind = KindUnreachable
	}
	return o
}

// Checked pings ep and hands back the classification beside a failure: the
// ping, KindNone and "" when it answers cleanly; otherwise the error as it
// came, its Kind and Message. The error is returned unchanged so a caller
// can still errors.As it.
func Checked(ctx context.Context, ep Endpoint) (modelapi.Ping, Kind, string, error) {
	p, err := Ping(ctx, ep)
	if err == nil {
		return p, KindNone, "", nil
	}
	o := Classify(err)
	return p, o.Kind, o.Message, err
}
