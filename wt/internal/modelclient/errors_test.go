package modelclient

import (
	"errors"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// TestClientErrorsAreModelpingErrors pins that modelclient's ping error names
// are aliases of modelping's: a caller holding *modelclient.SkewError (old
// code) still matches errors.As against *modelping.SkewError (the type the
// model side raises), and an Endpoint built in this package is a
// modelping.Endpoint. The boundary moved without breaking a caller's types.
func TestClientErrorsAreModelpingErrors(t *testing.T) {
	var ur *modelping.UnreachableError
	if !errors.As(error(&UnreachableError{Addr: "a"}), &ur) {
		t.Error("*UnreachableError does not As to *modelping.UnreachableError")
	}
	var sk *modelping.SkewError
	if !errors.As(error(&SkewError{Addr: "a"}), &sk) {
		t.Error("*SkewError does not As to *modelping.SkewError")
	}
	var ci *modelping.CredentialsInURLError
	if !errors.As(error(&CredentialsInURLError{}), &ci) {
		t.Error("*CredentialsInURLError does not As to *modelping.CredentialsInURLError")
	}
	var _ modelping.Endpoint = Endpoint{}
	var _ Endpoint = modelping.Endpoint{}
}
