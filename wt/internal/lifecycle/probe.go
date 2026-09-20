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
	"time"

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
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if responded, _ := e.probe(ctx, healthURL, 2*time.Second); !responded {
			if err := e.sleep(ctx, e.pollInterval); err != nil {
				return err
			}
			continue
		}
		if e.tryChat(ctx, chatURL, payload) {
			return nil
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("failed to warm up model %s at %s", model, chatURL)
}

func (e *env) tryChat(ctx context.Context, chatURL string, payload []byte) bool {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.chatClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return err == nil && chatCompletionMarker.Match(body)
}
