package tailer

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// pinnedLedgerSchemaVersion is the schema version the field set below belongs
// to. A LITERAL, not the constant — a pin that reads the value it pins cannot
// notice it moving.
const pinnedLedgerSchemaVersion = 6

// pinnedLedgerFieldSignature is the wire shape of LedgerState at
// pinnedLedgerSchemaVersion: one `jsonName:goType` line per field, sorted by
// json name. Regenerate by running the test and copying the "got" block; it is
// meant to be updated, just never silently.
const pinnedLedgerFieldSignature = `agent_version:string
background_procs:map[string]string
cum_by_model:map[string]*tailer.UsageBreakdown
cum_provider_cost_usd:float64
first_task_estimate:*tailer.TaskEstimate
first_user_text:string
last_assistant_text:string
last_event_type:string
last_offset:int64
last_task_estimate:*tailer.TaskEstimate
last_task_question:*tailer.TaskQuestion
last_task_summary:*tailer.TaskSummary
model_name:string
parser_state:*tailer.ParserLedger
pending_background_agent_count:int
pending_bash_polls:map[string]string
pending_task_creates:map[string]string
pending_waiting_cue:bool
resume_fingerprint:uint64
schema_version:int
session_error:*tailer.SessionError
task_seq:int
tasks:[]tailer.Task`

// TestLedgerState_FieldSetChangeRequiresASchemaDecision is the tripwire that
// #1815 needed and did not have.
//
// THE FAILURE MODE IT CLOSES IS SILENCE, NOT A WRONG ANSWER. Whether a new
// LedgerState field warrants a LedgerSchemaVersion bump is a genuine judgement
// call — the constant's doc lays out the rule, and the repo has landed on both
// sides of it correctly (4/5 bumped, #1104/#1150/#1076 did not). What has never
// existed is anything that makes the question get ASKED. Nothing pinned the
// literal version and nothing pinned the field set, so #1796 added SessionError,
// nobody weighed the bump, and the omission was indistinguishable from a
// deliberate decline for an entire release cycle.
//
// So this test deliberately does NOT try to decide the bump — it cannot, and a
// version of it that guessed would be worse than nothing. It fails on ANY field
// set change, and its message hands the next person the rule to apply.
func TestLedgerState_FieldSetChangeRequiresASchemaDecision(t *testing.T) {
	if LedgerSchemaVersion != pinnedLedgerSchemaVersion {
		t.Errorf("LedgerSchemaVersion = %d but the field set below is pinned at %d.\n"+
			"If you bumped the version, update pinnedLedgerSchemaVersion to match.",
			LedgerSchemaVersion, pinnedLedgerSchemaVersion)
	}

	got := ledgerFieldSignature(reflect.TypeOf(LedgerState{}))
	if got == pinnedLedgerFieldSignature {
		return
	}

	added, removed := signatureDiff(pinnedLedgerFieldSignature, got)
	t.Errorf(`LedgerState's persisted field set changed.
  added:   %v
  removed: %v

ANSWER THIS BEFORE UPDATING THE PIN: would the CURRENT parser read the bytes an
existing ledger has ALREADY CONSUMED differently than the parser that wrote it?

  no  -> the old ledger is degraded, not wrong. Do NOT bump; forcing a re-scan
         would discard every live session's accumulated cost to reclaim a
         shortcut. (ResumeFingerprint #1104, PendingWaitingCue #1150,
         PendingBackgroundAgentCount #1076.)
  yes -> BUMP LedgerSchemaVersion. The old summary is a verdict from a parser
         that could not see what this one sees, and the re-scan is the point,
         not the cost. Note that "it is just an additive omitempty field" does
         not settle this: versions 4 and 5 are both additive omitempty fields.
         (#649, #705, #1815.)

Then update pinnedLedgerFieldSignature (and pinnedLedgerSchemaVersion if you
bumped) to:

%s`, added, removed, got)
}

// TestLedgerFieldSignature_DetectsAnAddedField is the committed mutation for the
// tripwire above, which as a guard has no pre-fix red of its own.
//
// It builds LedgerState-plus-one-field at run time rather than hand-copying the
// struct, so the mutant cannot drift out of sync with the real type and quietly
// stop being "LedgerState + 1" — a hand-written copy would keep passing while
// testing a shape the daemon stopped using.
func TestLedgerFieldSignature_DetectsAnAddedField(t *testing.T) {
	real := reflect.TypeOf(LedgerState{})

	fields := make([]reflect.StructField, 0, real.NumField()+1)
	for i := 0; i < real.NumField(); i++ {
		fields = append(fields, real.Field(i))
	}
	fields = append(fields, reflect.StructField{
		Name: "SomeNewlyAddedField",
		Type: reflect.TypeOf(""),
		Tag:  `json:"some_newly_added_field,omitempty"`,
	})
	mutant := reflect.StructOf(fields)

	baseline := ledgerFieldSignature(real)
	mutated := ledgerFieldSignature(mutant)

	if mutated == baseline {
		t.Fatal("adding a field to LedgerState did not change its signature — " +
			"the tripwire above is inert and would not have caught #1815's missing bump")
	}
	added, removed := signatureDiff(baseline, mutated)
	if len(added) != 1 || added[0] != "some_newly_added_field:string" {
		t.Errorf("added = %v, want exactly [some_newly_added_field:string]", added)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want empty — nothing was taken away", removed)
	}
}

// TestLedgerFieldSignature_DetectsARetypedField covers the mutation the
// add/remove framing above would miss: a field kept under the same json name
// but given a different Go type. That is a wire-format change with no field
// count change at all, and it is exactly the shape most likely to be waved
// through as a refactor.
func TestLedgerFieldSignature_DetectsARetypedField(t *testing.T) {
	real := reflect.TypeOf(LedgerState{})

	fields := make([]reflect.StructField, 0, real.NumField())
	retyped := false
	for i := 0; i < real.NumField(); i++ {
		f := real.Field(i)
		if f.Name == "PendingWaitingCue" { // bool -> string
			f.Type = reflect.TypeOf("")
			retyped = true
		}
		fields = append(fields, f)
	}
	// The field this mutation targets must still exist, or the test proves
	// nothing while reporting success.
	if !retyped {
		t.Fatal("PendingWaitingCue is gone from LedgerState — this mutation " +
			"silently stopped mutating anything; retarget it at another field")
	}

	if ledgerFieldSignature(reflect.StructOf(fields)) == ledgerFieldSignature(real) {
		t.Error("retyping a field left the signature unchanged — a same-name, " +
			"different-type field would slip past the tripwire")
	}
}

// ledgerFieldSignature renders a struct's persisted shape as sorted
// `jsonName:goType` lines.
//
// Sorted by json name so a pure field REORDER — which changes nothing on the
// wire — does not trip the tripwire and train people to update the pin without
// reading it. Type is included because a same-name field with a new type is a
// schema change the name alone cannot show.
func ledgerFieldSignature(t reflect.Type) string {
	lines := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			name = f.Name // no json tag: encoding/json uses the Go name
		}
		if name == "-" {
			continue // never persisted, so not part of the schema
		}
		lines = append(lines, name+":"+f.Type.String())
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// lineSet indexes a signature's lines so membership is one map lookup.
func lineSet(sig string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(sig, "\n") {
		set[l] = true
	}
	return set
}

// linesNotIn returns sig's lines that set does not hold, in sig's own order.
func linesNotIn(sig string, set map[string]bool) []string {
	var out []string
	for _, l := range strings.Split(sig, "\n") {
		if !set[l] {
			out = append(out, l)
		}
	}
	return out
}

// signatureDiff reports which lines want has that got lacks, and vice versa.
func signatureDiff(want, got string) (added, removed []string) {
	return linesNotIn(got, lineSet(want)), linesNotIn(want, lineSet(got))
}
