package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// slotsFixture points XDG_CONFIG_HOME at a scratch dir and returns an app
// over the slot fixture registry, with the app's slot state read from disk.
func slotsFixture(t *testing.T, initial string) *app {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if initial != "" {
		if err := os.MkdirAll(filepath.Dir(profiles.SlotPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(profiles.SlotPath(), []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &app{cfg: slotFixtureConfig()}
}

func readSlotFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(profiles.SlotPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const slotsBase = `[[profile]]
name = "base"
agent = "claude"
default = "sonnet"

[profile.slots]
sonnet = "openrouter/a"
haiku = "openrouter/b"
`

// TestSlotsAddWritesProfile checks `slots add` end to end on a machine with
// no file: the entry is created with the given slots and env, the comment is
// written above it, and the command prints "created" plus the profile list
// section, which is what the skill reads back to confirm the write.
func TestSlotsAddWritesProfile(t *testing.T) {
	a := slotsFixture(t, "")
	out, err := runProfileCmd(t, a, "slots", "add", "mine", "--slot", "sonnet=openrouter/a", "--slot", "haiku=omlx/q", "--env", "FOO=bar", "--comment", "my note")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "created") || !strings.Contains(out, "claude: profile/mine ok") {
		t.Errorf("output = %q", out)
	}
	st, err := profiles.LoadSlots()
	if err != nil || len(st.Profiles) != 1 {
		t.Fatalf("LoadSlots = %+v, %v", st, err)
	}
	p := st.Profiles[0]
	if p.Agent != "claude" || p.Default != "sonnet" || p.Slots["haiku"] != "omlx/q" || p.Env["FOO"] != "bar" {
		t.Errorf("profile = %+v", p)
	}
	if !strings.Contains(readSlotFile(t), "# my note") {
		t.Error("comment not written")
	}
}

// TestSlotsAddPrintsBackupPath checks that adding to an existing file
// reports the .bak-<stamp> path rather than "created", so the user knows
// where their previous file lives.
func TestSlotsAddPrintsBackupPath(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	out, err := runProfileCmd(t, a, "slots", "add", "two", "--slot", "sonnet=openrouter/a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "backup: ") || !strings.Contains(out, ".bak-") {
		t.Errorf("output = %q", out)
	}
	if !strings.Contains(out, "profile/base ok") || !strings.Contains(out, "profile/two ok") {
		t.Errorf("list missing: %q", out)
	}
}

// TestSlotsAddRejectsUnknownModel checks that a slot naming an id the
// registry does not hold is refused and creates no file: the registry check
// is what makes a written profile launchable.
func TestSlotsAddRejectsUnknownModel(t *testing.T) {
	a := slotsFixture(t, "")
	_, err := runProfileCmd(t, a, "slots", "add", "mine", "--slot", "sonnet=nope/x")
	if err == nil || !strings.Contains(err.Error(), "nope/x") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(profiles.SlotPath()); serr == nil {
		t.Error("file was created")
	}
}

// TestSlotsAddRejectsBadName checks the name gate: a malformed name is
// refused up front, since `-M profile/<name>` must never read as a flag.
func TestSlotsAddRejectsBadName(t *testing.T) {
	a := slotsFixture(t, "")
	for _, n := range []string{"Bad", "-x", "a b", "a/b"} {
		if _, err := runProfileCmd(t, a, "slots", "add", n, "--slot", "sonnet=openrouter/a"); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
}

// TestSlotsAddRejectsBadSlotFlags checks the --slot/--env flag parsing: an
// undeclared slot, a value without "=", and a default that names an unfilled
// slot all error before the file is touched.
func TestSlotsAddRejectsBadSlotFlags(t *testing.T) {
	a := slotsFixture(t, "")
	for _, args := range [][]string{
		{"--slot", "bogus=openrouter/a"},
		{"--slot", "sonnet"},
		{"--slot", "sonnet=openrouter/a", "--env", "NOEQUALS"},
		{"--slot", "opus=openrouter/a"}, // no sonnet slot, default sonnet
	} {
		if _, err := runProfileCmd(t, a, append([]string{"slots", "add", "mine"}, args...)...); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
}

// TestSlotsNoConfig checks that the write commands refuse to run without a
// loaded registry: with no model ids to check slots against, a write could
// not be validated.
func TestSlotsNoConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := runProfileCmd(t, &app{}, "slots", "add", "x", "--slot", "sonnet=a"); err == nil {
		t.Error("nil cfg accepted")
	}
}

// TestSlotsSetChangesOneSlot checks that `set --slot` changes only the named
// slot's line: the other slots keep their ids, which is the surgical-edit
// guarantee the docs promise.
func TestSlotsSetChangesOneSlot(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	out, err := runProfileCmd(t, a, "slots", "set", "base", "--slot", "haiku=omlx/q")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	st, _ := profiles.LoadSlots()
	p := st.Profiles[0]
	if p.Slots["haiku"] != "omlx/q" || p.Slots["sonnet"] != "openrouter/a" {
		t.Errorf("slots = %v", p.Slots)
	}
	if !strings.Contains(out, "backup: ") || !strings.Contains(out, "profile/base ok") {
		t.Errorf("output = %q", out)
	}
}

// TestSlotsSetUnsetEnv checks the env round trip: --env adds keys to
// [profile.env] and --unset-env removes exactly the named one, so a swap
// can also retire an env entry.
func TestSlotsSetUnsetEnv(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "set", "base", "--env", "A=1", "--env", "B=2"); err != nil {
		t.Fatal(err)
	}
	if _, err := runProfileCmd(t, a, "slots", "set", "base", "--unset-env", "A"); err != nil {
		t.Fatal(err)
	}
	st, _ := profiles.LoadSlots()
	env := st.Profiles[0].Env
	if _, ok := env["A"]; ok || env["B"] != "2" {
		t.Errorf("env = %v", env)
	}
}

// TestSlotsSetDefault checks that --default rewrites the entry's default
// and the write still validates (the new default names a filled slot).
func TestSlotsSetDefault(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "set", "base", "--default", "haiku"); err != nil {
		t.Fatal(err)
	}
	st, _ := profiles.LoadSlots()
	if st.Profiles[0].Default != "haiku" {
		t.Errorf("default = %q", st.Profiles[0].Default)
	}
}

// TestSlotsSetUnknownName checks that `set` on a name the file does not
// hold is an error, never a silent no-op.
func TestSlotsSetUnknownName(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "set", "nope", "--default", "haiku"); err == nil {
		t.Error("unknown name accepted")
	}
}

// TestSlotsRmRemovesEntryKeepsOthers checks that `rm` deletes only the named
// entry: its neighbours survive and the printed list no longer names the
// removed profile.
func TestSlotsRmRemovesEntryKeepsOthers(t *testing.T) {
	a := slotsFixture(t, slotsBase+"\n[[profile]]\nname = \"other\"\nagent = \"claude\"\ndefault = \"sonnet\"\n\n[profile.slots]\nsonnet = \"openrouter/b\"\n")
	out, err := runProfileCmd(t, a, "slots", "rm", "base")
	if err != nil {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	st, _ := profiles.LoadSlots()
	if len(st.Profiles) != 1 || st.Profiles[0].Name != "other" {
		t.Errorf("profiles = %+v", st.Profiles)
	}
	if !strings.Contains(out, "backup: ") || strings.Contains(out, "profile/base") || !strings.Contains(out, "profile/other ok") {
		t.Errorf("output = %q", out)
	}
}

// TestSlotsRmUnknownName checks that `rm` of a name the file does not hold
// errors and leaves every byte of the file alone.
func TestSlotsRmUnknownName(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "rm", "nope"); err == nil {
		t.Error("unknown name accepted")
	}
	if readSlotFile(t) != slotsBase {
		t.Error("file changed")
	}
}

// TestSlotsWriteRefusedLeavesFileUnchanged checks a refused `set` from the
// CLI down to disk: the file is byte-identical and no backup was written,
// because the backup must exist only when the file actually changed.
func TestSlotsWriteRefusedLeavesFileUnchanged(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	_, err := runProfileCmd(t, a, "slots", "set", "base", "--slot", "haiku=nope/x")
	if err == nil {
		t.Fatal("refused write returned nil")
	}
	if readSlotFile(t) != slotsBase {
		t.Error("file changed")
	}
	if m, _ := filepath.Glob(profiles.SlotPath() + ".bak-*"); len(m) != 0 {
		t.Errorf("backup written: %v", m)
	}
}

// TestSlotsSetWithNothingToChangeRefusesBeforeTouchingFile checks the
// "nothing to change" guard: with no --slot/--default/--env/--unset-env,
// `set` errors before Apply runs, so the config dir gains nothing.
func TestSlotsSetWithNothingToChangeRefusesBeforeTouchingFile(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	before := readSlotFile(t)
	out, err := runProfileCmd(t, a, "slots", "set", "base")
	if err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("err = %v, out = %q", err, out)
	}
	if readSlotFile(t) != before {
		t.Error("file changed")
	}
	entries, _ := os.ReadDir(filepath.Dir(profiles.SlotPath()))
	if len(entries) != 1 {
		t.Errorf("backup written: %v", entries)
	}
}

const slotsTwo = `# the base note
# second line
[[profile]]
name = "base"
agent = "claude"
default = "sonnet"

[profile.slots]
sonnet = "openrouter/a"
haiku = "openrouter/b"

[[profile]]
name = "idle"
agent = "claude"
default = "sonnet"

[profile.slots]
sonnet = "omlx/q"
`

type usageTestRow struct {
	Name        string            `json:"name"`
	Launches30d int               `json:"launches_30d"`
	LastLaunch  *string           `json:"last_launch"`
	Comment     string            `json:"comment"`
	Slots       map[string]string `json:"slots"`
}

// TestSlotsUsageCountsAndLastLaunch checks the usage report against a
// seeded history: counts and the newest launch per profile in --json, and
// a table that lists every profile in file order — the skill's removal and
// review steps read exactly this.
func TestSlotsUsageCountsAndLastLaunch(t *testing.T) {
	a := slotsFixture(t, slotsTwo)
	st := usage.NewStoreAt(config.Dir())
	for i := 0; i < 3; i++ {
		if err := st.RecordFor("claude", "profile/base"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runProfileCmd(t, a, "slots", "usage", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []usageTestRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0].Name != "base" || rows[1].Name != "idle" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Launches30d != 3 || rows[0].LastLaunch == nil || !strings.HasSuffix(*rows[0].LastLaunch, "Z") {
		t.Errorf("base = %+v", rows[0])
	}
	if rows[0].Slots["haiku"] != "openrouter/b" {
		t.Errorf("slots = %v", rows[0].Slots)
	}
	table, err := runProfileCmd(t, a, "slots", "usage")
	if err != nil || !strings.Contains(table, "base") || !strings.Contains(table, "idle") {
		t.Errorf("table = %q, %v", table, err)
	}
}

// TestSlotsUsageNoLaunchesShowsZero checks that a profile with no recorded
// launch reports zero and a null last_launch rather than being skipped or
// erroring: zero means "not in the last 30 days", which is what cleanup
// decides on.
func TestSlotsUsageNoLaunchesShowsZero(t *testing.T) {
	a := slotsFixture(t, slotsTwo)
	out, err := runProfileCmd(t, a, "slots", "usage", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []usageTestRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Launches30d != 0 || r.LastLaunch != nil {
			t.Errorf("row = %+v", r)
		}
	}
	if !strings.Contains(out, `"last_launch": null`) {
		t.Errorf("want null last_launch: %s", out)
	}
}

// TestSlotsUsageIgnoresRenamedProfileHistory checks that rows are driven by
// the file, not the history: launches recorded under a profile name that no
// longer exists are not reported, so a rename does not resurrect old usage.
func TestSlotsUsageIgnoresRenamedProfileHistory(t *testing.T) {
	a := slotsFixture(t, slotsTwo)
	st := usage.NewStoreAt(config.Dir())
	if err := st.RecordFor("claude", "profile/old-name"); err != nil {
		t.Fatal(err)
	}
	out, err := runProfileCmd(t, a, "slots", "usage", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "old-name") {
		t.Errorf("renamed profile reported: %s", out)
	}
}

// TestSlotsUsageIncludesLeadingComment checks that each row carries the
// entry's leading comment verbatim: cleanup reads it to tell a one-off
// test profile from one that is kept on purpose.
func TestSlotsUsageIncludesLeadingComment(t *testing.T) {
	a := slotsFixture(t, slotsTwo)
	out, err := runProfileCmd(t, a, "slots", "usage", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []usageTestRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].Comment != "the base note\nsecond line" || rows[1].Comment != "" {
		t.Errorf("comments = %q, %q", rows[0].Comment, rows[1].Comment)
	}
}

type candOut struct {
	GeneratedAt *string  `json:"tiers_generated_at"`
	Stale       []string `json:"stale_sources"`
	Candidates  []struct {
		ID    string   `json:"id"`
		Score *float64 `json:"score"`
		Flags []string `json:"flags"`
	} `json:"candidates"`
}

const candTiers = `generated_at = "2026-10-12T15:58:00Z"

[sources.arena]
status = "stale"

[[models]]
provider_id = "openrouter"
model_name = "a"
tier = "deep"
code = "D"
confidence = "high"
aligned_score = 60
tool_call = true

[[models]]
provider_id = "omlx"
model_name = "q"
tier = "standard"
code = "S"
confidence = "high"
aligned_score = 55
tool_call = true

[[models]]
provider_id = "openrouter"
model_name = "b"
tier = "standard"
code = "S"
confidence = "high"
aligned_score = 50
tool_call = true
`

// TestSlotsCandidatesJSONIncludesFreshness checks that --json reports
// tiers.toml's generation time and stale sources beside the ranking, ranked
// open-weights-first: the skill refuses to trust a stale file it cannot see
// the age of.
func TestSlotsCandidatesJSONIncludesFreshness(t *testing.T) {
	a := slotsFixture(t, "")
	f := filepath.Join(t.TempDir(), "tiers.toml")
	if err := os.WriteFile(f, []byte(candTiers), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODELTIERS_OUT", f)
	out, err := runProfileCmd(t, a, "slots", "candidates", "--json", "--provider", "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	var r candOut
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.GeneratedAt == nil || *r.GeneratedAt != "2026-10-12T15:58:00Z" {
		t.Errorf("generated = %v", r.GeneratedAt)
	}
	if len(r.Stale) != 1 || r.Stale[0] != "arena" {
		t.Errorf("stale = %v", r.Stale)
	}
	if len(r.Candidates) != 2 || r.Candidates[0].ID != "openrouter/a" || r.Candidates[0].Score == nil {
		t.Errorf("candidates = %+v", r.Candidates)
	}
	table, err := runProfileCmd(t, a, "slots", "candidates", "--provider", "openrouter")
	if err != nil || !strings.HasPrefix(table, "tiers.toml: ") || !strings.Contains(table, "stale sources: arena") {
		t.Errorf("table = %q, %v", table, err)
	}
}

// TestSlotsCandidatesNoTiersFile checks the missing-tiers.toml path: every
// model is listed as unrated (score null, flagged) and the report says the
// file was not found — a degraded ranking, never a failed command.
func TestSlotsCandidatesNoTiersFile(t *testing.T) {
	a := slotsFixture(t, "")
	t.Setenv("MODELTIERS_OUT", filepath.Join(t.TempDir(), "missing.toml"))
	out, err := runProfileCmd(t, a, "slots", "candidates", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var r candOut
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.GeneratedAt != nil || len(r.Candidates) != 3 {
		t.Fatalf("result = %+v", r)
	}
	for _, c := range r.Candidates {
		if c.Score != nil || !slices.Contains(c.Flags, "unrated") {
			t.Errorf("candidate = %+v", c)
		}
	}
	table, err := runProfileCmd(t, a, "slots", "candidates")
	if err != nil || !strings.HasPrefix(table, "tiers.toml: not found") {
		t.Errorf("table = %q, %v", table, err)
	}
}

// TestSlotsCandidatesNearBothProviders checks --near across providers: one
// nearest model per provider, providers sorted by name, nearest first —
// the list the skill uses to build an ollama/openrouter twin pair.
func TestSlotsCandidatesNearBothProviders(t *testing.T) {
	a := slotsFixture(t, "")
	f := filepath.Join(t.TempDir(), "tiers.toml")
	if err := os.WriteFile(f, []byte(candTiers), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODELTIERS_OUT", f)
	out, err := runProfileCmd(t, a, "slots", "candidates", "--json", "--near", "openrouter/a", "--per-provider", "1")
	if err != nil {
		t.Fatal(err)
	}
	var r candOut
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Candidates {
		got = append(got, c.ID)
	}
	if !slices.Equal(got, []string{"omlx/q", "openrouter/b"}) {
		t.Errorf("got %v", got)
	}
}

// TestSlotsCandidatesAgeInWholeDays checks that the file's age prints in
// whole days, never as a raw Go duration, so the freshness line reads the
// same wherever the skill shows it.
func TestSlotsCandidatesAgeInWholeDays(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want string
	}{
		{30*24*time.Hour + 37*time.Minute, "tiers.toml: 30 days old\n"},
		{25 * time.Hour, "tiers.toml: 1 day old\n"},
		{2 * time.Hour, "tiers.toml: less than a day old\n"},
	}
	for _, c := range cases {
		a := slotsFixture(t, "")
		f := filepath.Join(t.TempDir(), "tiers.toml")
		gen := time.Now().Add(-c.age).UTC().Format(time.RFC3339)
		if err := os.WriteFile(f, []byte(strings.Replace(candTiers, "2026-10-12T15:58:00Z", gen, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MODELTIERS_OUT", f)
		out, err := runProfileCmd(t, a, "slots", "candidates")
		if err != nil || !strings.HasPrefix(out, c.want) {
			t.Errorf("age %v: out = %q, %v", c.age, out, err)
		}
	}
}

// TestSlotsCandidatesPerProviderNeedsNear checks that --per-provider without
// --near is refused: the count only means something with a reference model.
func TestSlotsCandidatesPerProviderNeedsNear(t *testing.T) {
	a := slotsFixture(t, "")
	_, err := runProfileCmd(t, a, "slots", "candidates", "--per-provider", "2")
	if err == nil || !strings.Contains(err.Error(), "--per-provider needs --near") {
		t.Fatalf("err = %v", err)
	}
}

// TestSlotsUsageJSONEmptySlotsIsObject checks that a profile with no slots
// serializes `"slots": {}` in JSON, not null: a null breaks typed consumers
// of the --json output.
func TestSlotsUsageJSONEmptySlotsIsObject(t *testing.T) {
	a := slotsFixture(t, "[[profile]]\nname = \"bare\"\nagent = \"claude\"\ndefault = \"sonnet\"\n")
	out, err := runProfileCmd(t, a, "slots", "usage", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"slots": {}`) || strings.Contains(out, `"slots": null`) {
		t.Errorf("out = %s", out)
	}
}
