package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// procWatch lets waitForModel notice that a serve process died mid-load.
type procWatch interface {
	exited() (done bool, err error)
}

var chatCompletionMarker = regexp.MustCompile(`"object"\s*:\s*"chat\.completion"`)

// probe does one GET. responded is true for ANY HTTP response (2xx or an error
// status: something is listening); timedOut is true when the listener accepted
// the connection and then stalled. A refused/reset connection is neither.
func (e *env) probe(ctx context.Context, url string, timeout time.Duration) (responded, timedOut bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, false
	}
	resp, err := e.probeClient.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
			return false, true
		}
		return false, false
	}
	_ = resp.Body.Close()
	return true, false
}

func (e *env) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// waitPortOpen polls url until it answers (any response counts), returning
// false (no error) when timeout passes first; ctx cancellation returns its error.
func (e *env) waitPortOpen(ctx context.Context, url string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if responded, _ := e.probe(ctx, url, time.Second); responded {
			return true, nil
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return false, err
		}
	}
	return false, ctx.Err()
}

// portClosedWithin polls url until it stops answering. A response — success or
// error status — or a read timeout means something still holds the port; only
// a connection-level failure means it closed.
func (e *env) portClosedWithin(ctx context.Context, url string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		responded, timedOut := e.probe(ctx, url, time.Second)
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !responded && !timedOut {
			return true, nil
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return false, err
		}
	}
	return false, ctx.Err()
}

// waitForModel polls modelsURL until it lists model (lenient name match), the
// serve process dies (when proc is given), or timeout passes.
func (e *env) waitForModel(ctx context.Context, modelsURL, model string, proc procWatch, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if proc != nil {
			if done, err := proc.exited(); done {
				return fmt.Errorf("serve process exited during model load: %v", err)
			}
		}
		for _, id := range localmodels.FetchModelIDs(e.probeClient, modelsURL) {
			if localmodels.NameMatches(id, model) {
				return nil
			}
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("timed out waiting for %s to serve %s", modelsURL, model)
}

// warmup forces model into memory: poll healthURL for liveness, then POST a
// 1-token chat completion to chatURL and require the chat.completion marker.
func (e *env) warmup(ctx context.Context, chatURL, model, healthURL string, timeout time.Duration) error {
	payload, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens":  1,
		"temperature": 0,
		"stream":      false,
	})
	deadline := time.Now().Add(timeout)
	var lastReason string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if responded, _ := e.probe(ctx, healthURL, 2*time.Second); !responded {
			lastReason = "health check at " + healthURL + " did not respond"
			if err := e.sleep(ctx, e.pollInterval); err != nil {
				return err
			}
			continue
		}
		ok, reason := e.tryChat(ctx, chatURL, payload)
		if ok {
			return nil
		}
		lastReason = reason
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lastReason == "" {
		lastReason = "no attempts completed"
	}
	return fmt.Errorf("failed to warm up model %s at %s: %s", model, chatURL, lastReason)
}

func (e *env) tryChat(ctx context.Context, chatURL string, payload []byte) (ok bool, reason string) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(payload))
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.chatClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "reading response: " + err.Error()
	}
	// A non-2xx answer is not a warmup, even when its body echoes a
	// chat.completion marker: the ported probe raises HTTPError here and
	// retries. Accepting it reports a model as resident that is not.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateForError(body))
	}
	if !chatCompletionMarker.Match(body) {
		return false, "response missing chat.completion marker: " + truncateForError(body)
	}
	return true, ""
}

// truncateForError bounds a server response body embedded in an error
// message: a warmup failure's body can be an HTML error page or a large
// JSON blob, and the caller's final error must stay a readable one-liner.
func truncateForError(b []byte) string {
	const max = 300
	// strings.Fields+Join below is O(len(b)); warmup polls once a second for
	// up to warmupTimeout (600s), so a failing server that keeps answering
	// with a multi-MB body (a misrouted proxy, an unrelated HTTP service on
	// the probed port) would otherwise pay a full-body allocate-and-split
	// pass on every poll purely to produce a 300-character message.
	// Whitespace collapsing only removes characters, never adds them, so
	// bounding the input to a fixed multiple of max bytes before splitting
	// still leaves far more than enough to fill max runes.
	const scanLimit = max * 8
	if len(b) > scanLimit {
		b = []byte(trimTrailingPartialRune(string(b[:scanLimit])))
	}
	// strings.Fields splits on any whitespace (including newlines/tabs) and
	// drops empty runs; joining with a single space collapses the body to
	// one line and trims its ends, so an HTML error page or a formatted
	// stack trace can't carry its line breaks into the caller's one-line
	// error message.
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) <= max {
		return s
	}
	return trimTrailingPartialRune(s[:max]) + "…"
}

// trimTrailingPartialRune backs off to a valid rune boundary: DecodeLastRuneInString
// reports (RuneError, 1) only when the trailing byte(s) are not a complete,
// valid encoding — the impossible-for-correct-UTF-8 signal that a byte cut
// landed mid-rune.
func trimTrailingPartialRune(s string) string {
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// liveServed asks a single-model provider's server directly what it is serving,
// and reports whether that answer can be trusted.
//
// known is false only when the server failed to give a usable answer — it
// accepted the connection and stalled, or answered with a non-2xx or
// undecodable body. A refused or reset connection is NOT unknown: it means
// nothing is listening, so known is true with no ids, and an ordinary cold
// start proceeds without a confirmation. Collapsing those two cases either
// refuses every cold start or permits the silent replacement this exists to
// prevent.
func (e *env) liveServed(ctx context.Context, cfg *config.Config, family string) (ids []string, known bool) {
	origin, _ := localmodels.FamilyOrigin(cfg, family)
	modelsURL := origin + "/v1/models"
	responded, timedOut := e.probe(ctx, modelsURL, e.prebindTimeout)
	switch {
	case responded:
		ids, err := localmodels.FetchModelIDsErr(e.probeClient, modelsURL)
		if err != nil {
			return nil, false
		}
		return ids, true
	case timedOut:
		return nil, false
	default:
		// Connection refused/reset: definitively nothing listening.
		return nil, true
	}
}
