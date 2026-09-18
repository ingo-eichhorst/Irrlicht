package validate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"irrlicht/core/application/replayengine"
	"irrlicht/tools/onboarding-factory/internal/matrix"
	internalreplay "irrlicht/tools/onboarding-factory/internal/replay"
)

// RecordAssertion counts JSONL records that match every dotted-path predicate.
// MaxCount is a pointer so a configured zero remains distinct from no maximum.
type RecordAssertion struct {
	Name          string                   `json:"name"`
	KnownFailing  bool                     `json:"known_failing,omitempty"`
	Where         map[string]any           `json:"where"`
	Contains      map[string]string        `json:"contains,omitempty"`
	MinCount      int                      `json:"min_count,omitempty"`
	MaxCount      *int                     `json:"max_count,omitempty"`
	Related       *RelatedRecordAssertion  `json:"related,omitempty"`
	EqualPaths    []string                 `json:"equal_paths,omitempty"`
	MinDistinct   map[string]int           `json:"min_distinct,omitempty"`
	FieldContains []FieldContainsAssertion `json:"field_contains,omitempty"`
}

// RelatedRecordAssertion requires each selected record to have a second
// selected record whose TargetPath equals the first record's SourcePath.
type RelatedRecordAssertion struct {
	Where      map[string]any    `json:"where"`
	Contains   map[string]string `json:"contains,omitempty"`
	SourcePath string            `json:"source_path"`
	TargetPath string            `json:"target_path"`
}

// FieldContainsAssertion requires one string field in each selected record to
// contain another string field from that same record.
type FieldContainsAssertion struct {
	ValuePath     string `json:"value_path"`
	ContainerPath string `json:"container_path"`
}

type RecordAssertResult struct {
	Name         string `json:"name"`
	Expected     string `json:"expected"`
	Actual       string `json:"actual"`
	OK           bool   `json:"ok"`
	KnownFailing bool   `json:"known_failing,omitempty"`
}

type RecordReport struct {
	Pass    bool                 `json:"pass"`
	Asserts []RecordAssertResult `json:"asserts"`
}

// ExpectedPass reports whether every failure is explicitly expected on its
// own assertion. One known defect cannot waive a different assertion.
func (r *RecordReport) ExpectedPass() bool {
	if r == nil {
		return true
	}
	for _, assertion := range r.Asserts {
		if !assertion.OK && !assertion.KnownFailing {
			return false
		}
	}
	return true
}

// ValidateTranscriptForProfile checks the newest recording's durable
// transcript against the assertions in expected.jsonl. It returns nil when a
// cell declares no transcript assertions.
func ValidateTranscriptForProfile(scenarioDir string, profile matrix.ExecutionProfile) (*RecordReport, error) {
	meta, err := loadExpectedMeta(scenarioDir)
	if err != nil || meta == nil || len(meta.TranscriptAssertions) == 0 {
		return nil, err
	}
	recordings, err := matrix.RecordingsForProfile(scenarioDir, profile)
	if err != nil {
		return nil, err
	}
	if len(recordings) == 0 {
		return nil, fmt.Errorf("transcript assertions configured but no %s recording exists", profile)
	}
	path := internalreplay.TranscriptPath(recordings[0].Dir)
	if path == "" || strings.HasSuffix(path, ".md") {
		return nil, fmt.Errorf("transcript assertions configured but newest recording has no JSONL transcript")
	}
	records, err := readJSONLRecords(path, "transcript")
	if err != nil {
		return nil, err
	}
	return evaluateRecordAssertions("transcript", meta.TranscriptAssertions, records)
}

// ValidateEventsForProfile checks the newest recording's events against the
// event assertions in expected.jsonl. It returns nil when none are declared.
func ValidateEventsForProfile(scenarioDir string, profile matrix.ExecutionProfile) (*RecordReport, error) {
	meta, err := loadExpectedMeta(scenarioDir)
	if err != nil || meta == nil || len(meta.EventAssertions) == 0 {
		return nil, err
	}
	recording, ok, err := matrix.NewestRecording(scenarioDir, profile)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("event assertions configured but no %s recording exists", profile)
	}
	path := filepath.Join(recording.Dir, "events.jsonl")
	records, err := readJSONLRecords(path, "event")
	if err != nil {
		return nil, err
	}
	return evaluateRecordAssertions("event", meta.EventAssertions, records)
}

func loadExpectedMeta(scenarioDir string) (*ExpectedMeta, error) {
	b, err := os.ReadFile(filepath.Join(scenarioDir, "expected.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range splitLines(b) {
		if len(line) == 0 {
			continue
		}
		var meta ExpectedMeta
		if err := json.Unmarshal(line, &meta); err != nil {
			return nil, fmt.Errorf("parse expected meta: %w", err)
		}
		return &meta, nil
	}
	return nil, nil
}

func validateRecordAssertionSpec(kind string, assertion RecordAssertion) error {
	if assertion.Name == "" {
		return fmt.Errorf("%s assertion has no name", kind)
	}
	if len(assertion.Where) == 0 && len(assertion.Contains) == 0 {
		return fmt.Errorf("%s assertion %q has no selector", kind, assertion.Name)
	}
	if assertion.MinCount < 0 || assertion.MaxCount != nil && *assertion.MaxCount < assertion.MinCount {
		return fmt.Errorf("%s assertion %q has an invalid count range", kind, assertion.Name)
	}
	if assertion.Related != nil {
		if assertion.Related.SourcePath == "" || assertion.Related.TargetPath == "" ||
			len(assertion.Related.Where) == 0 && len(assertion.Related.Contains) == 0 {
			return fmt.Errorf("%s assertion %q has an incomplete related-record selector", kind, assertion.Name)
		}
	}
	for path, minimum := range assertion.MinDistinct {
		if path == "" || minimum < 1 {
			return fmt.Errorf("%s assertion %q has an invalid distinct-value requirement", kind, assertion.Name)
		}
	}
	for _, relation := range assertion.FieldContains {
		if relation.ValuePath == "" || relation.ContainerPath == "" {
			return fmt.Errorf("%s assertion %q has an incomplete field-contains requirement", kind, assertion.Name)
		}
	}
	return nil
}

func readJSONLRecords(path, kind string) ([]map[string]any, error) {
	var records []map[string]any
	err := replayengine.ScanTranscriptLines(path, func(line []byte) error {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("parse %s record: %w", kind, err)
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s records: %w", kind, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%s assertions configured but recording has no records", kind)
	}
	return records, nil
}

func evaluateRecordAssertions(kind string, assertions []RecordAssertion, records []map[string]any) (*RecordReport, error) {
	report := &RecordReport{Pass: true}
	for _, assertion := range assertions {
		if err := validateRecordAssertionSpec(kind, assertion); err != nil {
			return nil, err
		}
		result := evaluateRecordAssertion(assertion, records)
		report.Asserts = append(report.Asserts, result)
		report.Pass = report.Pass && result.OK
	}
	return report, nil
}

func evaluateRecordAssertion(assertion RecordAssertion, records []map[string]any) RecordAssertResult {
	matched := selectRecords(records, assertion.Where, assertion.Contains)
	ok := len(matched) >= assertion.MinCount
	expected := fmt.Sprintf("count >= %d", assertion.MinCount)
	if assertion.MaxCount != nil {
		ok = ok && len(matched) <= *assertion.MaxCount
		expected += fmt.Sprintf(" and <= %d", *assertion.MaxCount)
	}
	details := fmt.Sprintf("count = %d", len(matched))

	if assertion.Related != nil {
		related := selectRecords(records, assertion.Related.Where, assertion.Related.Contains)
		for _, record := range matched {
			value, exists := valueAtPath(record, assertion.Related.SourcePath)
			if !exists || !recordsContainValue(related, assertion.Related.TargetPath, value) {
				ok = false
			}
		}
		expected += ", each with a related record"
	}
	for _, path := range assertion.EqualPaths {
		if !allPathValuesEqual(matched, path) {
			ok = false
		}
		expected += ", equal " + path
	}
	for path, minimum := range assertion.MinDistinct {
		count := distinctPathValues(matched, path)
		if count < minimum {
			ok = false
		}
		details += fmt.Sprintf(", distinct %s = %d", path, count)
		expected += fmt.Sprintf(", distinct %s >= %d", path, minimum)
	}
	for _, relation := range assertion.FieldContains {
		if !allFieldsContain(matched, relation.ValuePath, relation.ContainerPath) {
			ok = false
		}
		expected += fmt.Sprintf(", each %s contains %s", relation.ContainerPath, relation.ValuePath)
	}

	name := assertion.Name
	if name == "" {
		name = "records"
	}
	return RecordAssertResult{
		Name: name, Expected: expected, Actual: details, OK: ok,
		KnownFailing: assertion.KnownFailing,
	}
}

func selectRecords(records []map[string]any, where map[string]any, contains map[string]string) []map[string]any {
	var selected []map[string]any
	for _, record := range records {
		if recordMatches(record, where, contains) {
			selected = append(selected, record)
		}
	}
	return selected
}

func allFieldsContain(records []map[string]any, valuePath, containerPath string) bool {
	if len(records) == 0 {
		return false
	}
	for _, record := range records {
		value, valueOK := valueAtPath(record, valuePath)
		container, containerOK := valueAtPath(record, containerPath)
		valueText, valueString := value.(string)
		containerText, containerString := container.(string)
		if !valueOK || !containerOK || !valueString || !containerString || !strings.Contains(containerText, valueText) {
			return false
		}
	}
	return true
}

func recordMatches(record map[string]any, where map[string]any, contains map[string]string) bool {
	for path, expected := range where {
		actual, ok := valueAtPath(record, path)
		if !ok || !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	for path, expected := range contains {
		actual, ok := valueAtPath(record, path)
		text, isString := actual.(string)
		if !ok || !isString || !strings.Contains(text, expected) {
			return false
		}
	}
	return true
}

func valueAtPath(root any, path string) (any, bool) {
	current := root
	for _, part := range strings.Split(path, ".") {
		switch value := current.(type) {
		case map[string]any:
			current, _ = value[part]
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
		if current == nil {
			return nil, false
		}
	}
	return current, true
}

func recordsContainValue(records []map[string]any, path string, expected any) bool {
	for _, record := range records {
		if actual, ok := valueAtPath(record, path); ok && reflect.DeepEqual(actual, expected) {
			return true
		}
	}
	return false
}

func allPathValuesEqual(records []map[string]any, path string) bool {
	if len(records) == 0 {
		return false
	}
	first, ok := valueAtPath(records[0], path)
	if !ok {
		return false
	}
	for _, record := range records[1:] {
		value, ok := valueAtPath(record, path)
		if !ok || !reflect.DeepEqual(value, first) {
			return false
		}
	}
	return true
}

func distinctPathValues(records []map[string]any, path string) int {
	values := map[string]struct{}{}
	for _, record := range records {
		if value, ok := valueAtPath(record, path); ok {
			encoded, _ := json.Marshal(value)
			values[string(encoded)] = struct{}{}
		}
	}
	return len(values)
}
