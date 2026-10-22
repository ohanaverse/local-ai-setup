package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestEventFieldsMatchSharedFixture guards the JSON shape of a usage.jsonl
// line. The fixture at docs/contracts/usage.sample.jsonl is read by this
// test: a rename of model_id or timestamp would leave every line already on
// disk unreadable, and wt's usage history and `wt stats` would come up empty.
func TestEventFieldsMatchSharedFixture(t *testing.T) {
	f, err := os.Open("../../../docs/contracts/usage.sample.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var events []event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].ModelID != "ollama/contract-fixture:local" {
		t.Errorf("events[0].ModelID = %q, want %q", events[0].ModelID, "ollama/contract-fixture:local")
	}
	wantTS := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if !events[0].Timestamp.Equal(wantTS) {
		t.Errorf("events[0].Timestamp = %v, want %v", events[0].Timestamp, wantTS)
	}
}
