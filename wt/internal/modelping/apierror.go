package modelping

import (
	"encoding/json"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// DecodeAPIError decodes a control answer body that carries the server's own
// error, `{"error":{"code":...,"message":...}}`. It is the one decode rule for
// both readers: modelping's GetJSON and modelclient's statusErr. ok is false
// for a body that is not that shape, or an error with no code: those are not
// APIError answers, and each caller keeps its own classification for them. The
// returned fields are Cleaned, so both paths print the server's answer with
// the hygiene modelclient has always applied.
func DecodeAPIError(body []byte) (ae *modelapi.APIError, ok bool) {
	var e modelapi.APIError
	if err := json.Unmarshal(body, &e); err != nil || e.Error.Code == "" {
		return nil, false
	}
	e.Error.Code, e.Error.Message = Clean(e.Error.Code), Clean(e.Error.Message)
	return &e, true
}
