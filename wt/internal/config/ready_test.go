package config

import "testing"

// TestReadyFlagReadsModelmanState pins that ReadyFlag mirrors modelman's
// per-model `ready` flag (legacy `downloaded` ORed at load). The LiteLLM
// expose gate depends on it: a wrong answer would either route models that
// are not on disk or refuse ones that are.
func TestReadyFlagReadsModelmanState(t *testing.T) {
	c := &Config{}
	c.SetExposureForTest("a/b", ExposureEntry{Ready: true})
	if !c.ReadyFlag("a/b") {
		t.Fatal("ReadyFlag(a/b) = false, want true")
	}
	if c.ReadyFlag("missing") {
		t.Fatal("ReadyFlag(missing) = true, want false")
	}
}
