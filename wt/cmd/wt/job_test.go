package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

const jobFixture = "../../../docs/contracts/job.sample.json"

// jobRig is a fake model server holding fixed jobs, an app pointed at it, and
// polling that does not wait.
type jobRig struct {
	*remoteRig
}

func newJobRig(t *testing.T, jobs ...modelapi.JobStatus) *jobRig {
	t.Helper()
	r := newRemoteRig(t)
	r.f.Fixed = map[string]modelapi.JobStatus{}
	for _, j := range jobs {
		r.f.Fixed[j.ID] = j
	}
	old := jobSleep
	jobSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { jobSleep = old })
	return &jobRig{r}
}

// run executes `wt job args…`, returning stdout and the error.
func (r *jobRig) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := jobCmd(r.a)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return out.String(), err
}

func jobStatus(id, state, step string) modelapi.JobStatus {
	return modelapi.JobStatus{ID: id, Kind: "start", Model: "omlx/q", State: state, Step: step,
		Created: "2026-11-16T10:00:00Z", Updated: "2026-11-16T10:00:05Z"}
}

func failedJob(id string) modelapi.JobStatus {
	j := jobStatus(id, modelapi.JobFailed, "failed")
	j.Result = &modelapi.JobResult{Error: &modelapi.Problem{Code: modelapi.CodeFailed, Message: "omlx/q did not answer within 400s"}}
	return j
}

// TestJobShowsOnStdoutWithoutPrefix proves the result is one stdout line
// without the "wt:" prefix (M1/N1), that nothing is on stderr for a plain
// show, and that exit is 0 for a running job.
func TestJobShowsOnStdoutWithoutPrefix(t *testing.T) {
	r := newJobRig(t, jobStatus("job-1", modelapi.JobRunning, "loading the model"))
	out, err := r.run(t, "job-1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out != "job job-1 — running — loading the model\n" {
		t.Errorf("stdout = %q", out)
	}
	if r.stderr.String() != "" {
		t.Errorf("stderr = %q, want none", r.stderr.String())
	}
}

// TestJobFailedExits2AndCancelledExits2 proves a failed job and a cancelled
// job both exit 2, a succeeded one 0, and a failed job's problem is the
// stderr error.
func TestJobFailedExits2AndCancelledExits2(t *testing.T) {
	cancelled := jobStatus("job-c", modelapi.JobCancelled, "cancelled")
	r := newJobRig(t, failedJob("job-f"), cancelled, jobStatus("job-s", modelapi.JobSucceeded, "ready"))
	out, err := r.run(t, "job-f")
	if exitcode.Of(err) != 2 || err == nil || err.Error() != "omlx/q did not answer within 400s" {
		t.Errorf("failed: err = %v (exit %d)", err, exitcode.Of(err))
	}
	if out != "job job-f — failed — failed\n" {
		t.Errorf("failed stdout = %q", out)
	}
	if _, err := r.run(t, "job-c"); exitcode.Of(err) != 2 {
		t.Errorf("cancelled: exit %d, want 2 (err %v)", exitcode.Of(err), err)
	}
	if _, err := r.run(t, "job-s"); err != nil {
		t.Errorf("succeeded: err = %v, want exit 0", err)
	}
}

// TestJobCancelPollsToATerminalState proves --cancel posts the cancel, polls
// (progress on stderr with the prefix) until the job is cancelled, prints the
// result on stdout and exits 2.
func TestJobCancelPollsToATerminalState(t *testing.T) {
	r := newJobRig(t, jobStatus("job-1", modelapi.JobRunning, "loading the model"))
	r.f.CancelRunning = 2
	out, err := r.run(t, "job-1", "--cancel")
	if exitcode.Of(err) != 2 {
		t.Fatalf("err = %v (exit %d), want exit 2", err, exitcode.Of(err))
	}
	if out != "job job-1 — cancelled — cancelled\n" {
		t.Errorf("stdout = %q", out)
	}
	if got := r.f.Cancels; len(got) != 1 || got[0] != "job-1" {
		t.Errorf("cancels = %v", got)
	}
	if !strings.Contains(r.stderr.String(), "wt: cancelling job job-1 — running (cancelling)") {
		t.Errorf("stderr = %q, want the progress with the prefix", r.stderr.String())
	}
	if strings.Contains(out, "wt:") {
		t.Errorf("stdout carries the prefix: %q", out)
	}
}

// TestJobUnknownIdTexts proves an unknown id exits 1 with the *JobLostError
// text for a show and the "nothing was cancelled" text for --cancel.
func TestJobUnknownIdTexts(t *testing.T) {
	r := newJobRig(t)
	_, err := r.run(t, "nope")
	if exitcode.Of(err) != 1 || err == nil || err.Error() != "the model server no longer knows job nope (it finished and was dropped from its list, or the server restarted) — run 'wt model list --server' to see whether the model came up" {
		t.Errorf("show: err = %v (exit %d)", err, exitcode.Of(err))
	}
	_, err = r.run(t, "nope", "--cancel")
	const want = "no job nope on the model server — it is unknown, finished and dropped from the list, or the server restarted; nothing was cancelled"
	if exitcode.Of(err) != 1 || err == nil || err.Error() != want {
		t.Errorf("cancel: err = %v (exit %d), want %q", err, exitcode.Of(err), want)
	}
}

// fixtureMembers is job.sample.json's named members, raw.
func fixtureMembers(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(jobFixture)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return m
}

func asAny(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return v
}

// TestJobJSONMatchesTheFixture proves `wt job --json` prints exactly the
// documents docs/contracts/job.sample.json holds, for a succeeded job (exit
// 0), a failed one (the same job envelope, exit 2, quiet) and an unknown id
// (the error envelope, exit 1, quiet), and that each member says what its name
// claims.
func TestJobJSONMatchesTheFixture(t *testing.T) {
	ok := jobStatus("job-1", modelapi.JobSucceeded, "ready")
	ok.Result = &modelapi.JobResult{OK: true}
	r := newJobRig(t, ok, failedJob("job-2"))
	members := fixtureMembers(t)
	for name, tc := range map[string]struct {
		id   string
		code int
	}{"success": {"job-1", 0}, "failed": {"job-2", 2}, "error": {"job-9", 1}} {
		t.Run(name, func(t *testing.T) {
			raw, ok := members[name]
			if !ok {
				t.Fatalf("fixture has no %q member", name)
			}
			out, err := r.run(t, tc.id, "--json")
			if exitcode.Of(err) != tc.code && (tc.code != 0 || err != nil) {
				t.Fatalf("exit %d (err %v), want %d", exitcode.Of(err), err, tc.code)
			}
			if tc.code != 0 && !exitcode.IsQuiet(err) {
				t.Errorf("err = %v is not quiet: the document is the failure report (J4)", err)
			}
			if !reflect.DeepEqual(asAny(t, []byte(out)), asAny(t, raw)) {
				t.Errorf("document differs from the fixture\ngot:  %s\nwant: %s", out, raw)
			}
			if !strings.HasPrefix(out, "{\n  \"version\": 1,") || !strings.HasSuffix(out, "}\n") {
				t.Errorf("not one two-space-indented document (J1):\n%s", out)
			}
			var env struct {
				Version int                   `json:"version"`
				Job     *modelapi.JobStatus   `json:"job"`
				Error   *modelapi.ErrorObject `json:"error"`
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&env); err != nil {
				t.Fatalf("fixture member %s: %v", name, err)
			}
			switch name {
			case "success":
				if env.Job == nil || env.Job.State != "succeeded" || env.Error != nil {
					t.Errorf("success member says %+v", env)
				}
			case "failed":
				if env.Job == nil || env.Job.State != "failed" || env.Error != nil {
					t.Errorf("failed member says %+v", env)
				}
			case "error":
				if env.Job != nil || env.Error == nil || env.Error.Code != modelapi.CodeUnknownJob {
					t.Errorf("error member says %+v", env)
				}
			}
		})
	}
	if len(members) != 3 {
		t.Errorf("fixture has %d members, want success, failed and error", len(members))
	}
}

// TestJobJSONFailureEnvelope proves a failure under --json (an unreachable
// server) prints the error envelope on stdout with exit 1 and a quiet error,
// and nothing on stderr.
func TestJobJSONFailureEnvelope(t *testing.T) {
	r := newJobRig(t)
	r.f.Close()
	out, err := r.run(t, "job-1", "--json")
	if exitcode.Of(err) != 1 || !exitcode.IsQuiet(err) {
		t.Fatalf("err = %v (exit %d, quiet %v), want exit 1 quiet", err, exitcode.Of(err), exitcode.IsQuiet(err))
	}
	var env struct {
		Version int
		Job     *json.RawMessage
		Error   struct{ Message, Code string }
	}
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	if env.Version != 1 || env.Job != nil || !strings.Contains(env.Error.Message, "is not reachable") || env.Error.Code == "" {
		t.Errorf("envelope = %+v", env)
	}
	if r.stderr.String() != "" {
		t.Errorf("stderr = %q, want none", r.stderr.String())
	}
}

// TestJobHelpHasLongAndExample pins C2: the job command's Use and Short, a
// Long that names the flags, the exit codes and the schema path, and an
// Example with the three forms.
func TestJobHelpHasLongAndExample(t *testing.T) {
	c := jobCmd(&app{})
	if c.Use != "job ID" || c.Short != "Show or cancel a model server job" {
		t.Errorf("Use/Short = %q / %q", c.Use, c.Short)
	}
	for _, want := range []string{"--cancel", "--json", "Exit 0", "docs/contracts/job.sample.json"} {
		if !strings.Contains(c.Long, want) {
			t.Errorf("Long lacks %q", want)
		}
	}
	for _, want := range []string{"wt job ", "--cancel", "--json"} {
		if !strings.Contains(c.Example, want) {
			t.Errorf("Example lacks %q", want)
		}
	}
}
