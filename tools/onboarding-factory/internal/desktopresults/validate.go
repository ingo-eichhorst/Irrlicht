package desktopresults

import (
	"encoding/json"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

const (
	claudeAdapter = "claudecode"
	resultsSwitch = "required_execution_profiles_by_agent"
	metadataFile  = "metadata.json"
)

// Finding locates one invalid result or one broken evidence edge.
type Finding struct {
	Path    string
	Message string
}

type catalogScenario struct {
	Name string `json:"name"`
}

type catalogDocument struct {
	Meta      map[string]json.RawMessage `json:"meta"`
	Scenarios []catalogScenario          `json:"scenarios"`
}

type requiredProfilesInput struct {
	raw  json.RawMessage
	path string
}

type requiredProfileList struct {
	adapter  string
	profiles []string
	path     string
}

type cellCandidate struct {
	root string
	path string
}

type findingTarget struct {
	path     string
	scenario string
}

type resultFileContext struct {
	target            findingTarget
	cellDir           string
	expectedScenario  string
	canonicalScenario string
	canonical         bool
}

type resultValidationContext struct {
	target  findingTarget
	cellDir string
	result  *Result
}

type resultDocumentLocation struct {
	path   string
	target findingTarget
}

type scenarioValidation struct {
	target   findingTarget
	expected string
	actual   string
}

type evidenceBoundary struct {
	cellDir  string
	resolved string
}

type pathReference struct {
	root      string
	reference string
}

type pathPair struct {
	left  string
	right string
}

type validation struct {
	repoRoot       string
	catalog        map[string]bool
	claudeCells    map[string]bool
	canonicalCells map[string]string
	resultCount    map[string]int
	requireDesktop bool
	findings       []Finding
}

// ValidateRepo validates every present Claude Desktop result independently of
// cell metadata. If the catalog enables Desktop completeness, it also requires
// exactly one result for every current catalog scenario.
func ValidateRepo(repoRoot string) []Finding {
	v := &validation{
		repoRoot:       repoRoot,
		catalog:        map[string]bool{},
		claudeCells:    map[string]bool{},
		canonicalCells: map[string]string{},
		resultCount:    map[string]int{},
	}
	v.loadCatalog()
	v.scanResultFiles()
	v.validateCompleteness()
	sort.Slice(v.findings, func(i, j int) bool {
		if v.findings[i].Path != v.findings[j].Path {
			return v.findings[i].Path < v.findings[j].Path
		}
		return v.findings[i].Message < v.findings[j].Message
	})
	return v.findings
}

func (v *validation) add(target findingTarget, field, message string) {
	if target.scenario == "" {
		target.scenario = "unknown"
	}
	v.findings = append(v.findings, Finding{
		Path:    filepath.ToSlash(target.path),
		Message: fmt.Sprintf("scenario_id %q: %s: %s", target.scenario, field, message),
	})
}

func (v *validation) loadCatalog() {
	path := filepath.Join(v.repoRoot, "replaydata", "agents", "scenarios.json")
	var catalog catalogDocument
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &catalog) != nil {
		return // cmd/of's catalog validator owns this primary parse failure.
	}
	for _, scenario := range catalog.Scenarios {
		if scenario.Name != "" {
			v.catalog[scenario.Name] = true
		}
	}
	v.validateRequiredProfiles(requiredProfilesInput{raw: catalog.Meta[resultsSwitch], path: path})
}

func (v *validation) validateRequiredProfiles(input requiredProfilesInput) {
	if len(input.raw) == 0 {
		return // absence is the legacy CLI-only compatibility contract.
	}
	required, err := decodeRequiredProfiles(input.raw)
	if err != nil {
		v.add(findingTarget{path: v.rel(input.path), scenario: "catalog"}, "meta."+resultsSwitch, err.Error())
		return
	}
	for adapter, profiles := range required {
		v.validateRequiredProfileList(requiredProfileList{adapter: adapter, profiles: profiles, path: input.path})
	}
}

func decodeRequiredProfiles(raw json.RawMessage) (map[string][]string, error) {
	if string(raw) == "null" {
		return nil, fmt.Errorf("must be a non-empty object")
	}
	var required map[string][]string
	if err := json.Unmarshal(raw, &required); err != nil {
		return nil, fmt.Errorf("must be a non-empty object")
	}
	if len(required) == 0 {
		return nil, fmt.Errorf("must be a non-empty object")
	}
	return required, nil
}

func (v *validation) validateRequiredProfileList(input requiredProfileList) {
	field := "meta." + resultsSwitch + "." + input.adapter
	target := findingTarget{path: v.rel(input.path), scenario: "catalog"}
	if input.adapter != claudeAdapter {
		v.add(target, field, "Desktop result completeness is only supported for claudecode")
		return
	}
	if len(input.profiles) == 0 {
		v.add(target, field, "must contain at least one profile")
		return
	}
	seen := map[matrix.ExecutionProfile]bool{}
	for _, value := range input.profiles {
		profile, err := matrix.ParseExecutionProfile(value)
		if err != nil {
			v.add(target, field, err.Error())
			continue
		}
		if profile != matrix.ProfileDesktopLocal {
			v.add(target, field, "only desktop-local can be required by this result contract")
			continue
		}
		if seen[profile] {
			v.add(target, field, "contains duplicate desktop-local")
			continue
		}
		seen[profile] = true
		v.requireDesktop = true
	}
}

func (v *validation) scanResultFiles() {
	root := filepath.Join(v.repoRoot, "replaydata", "agents", claudeAdapter)
	var resultFiles []string
	err := filepath.WalkDir(root, func(path string, entry iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			v.add(findingTarget{path: v.rel(path), scenario: "unknown"}, "scan", walkErr.Error())
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		switch entry.Name() {
		case metadataFile:
			v.registerClaudeCell(cellCandidate{root: root, path: path})
		case FileName:
			resultFiles = append(resultFiles, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		v.add(findingTarget{path: v.rel(root), scenario: "unknown"}, "scan", err.Error())
	}
	for _, path := range resultFiles {
		v.validateResultFile(path)
	}
}

func (v *validation) registerClaudeCell(candidate cellCandidate) {
	rel, err := filepath.Rel(candidate.root, candidate.path)
	if err != nil {
		return
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if !isCanonicalCellMetadata(parts) {
		return
	}
	scenario := metadataScenarioID(filepath.Dir(candidate.path))
	if v.catalog[scenario] {
		v.claudeCells[scenario] = true
		v.canonicalCells[filepath.Clean(filepath.Dir(candidate.path))] = scenario
	}
}

func isCanonicalCellMetadata(parts []string) bool {
	if len(parts) != 3 {
		return false
	}
	if parts[0] != "scenarios" {
		return false
	}
	return parts[2] == metadataFile
}

func (v *validation) validateResultFile(path string) {
	rel := v.rel(path)
	cellDir := filepath.Clean(filepath.Dir(path))
	expectedScenario := metadataScenarioID(cellDir)
	target := findingTarget{path: rel, scenario: expectedScenario}
	canonicalScenario, canonical := v.canonicalCells[cellDir]
	if !canonical {
		v.add(target, "document location", "must be the direct child of a current Claude Code cell")
	}
	doc, ok := v.loadResultDocument(resultDocumentLocation{path: path, target: target})
	if !ok {
		return
	}
	context := resultFileContext{
		target:            target,
		cellDir:           cellDir,
		expectedScenario:  expectedScenario,
		canonicalScenario: canonicalScenario,
		canonical:         canonical,
	}
	v.validateResults(context, doc.Results)
}

func (v *validation) loadResultDocument(location resultDocumentLocation) (Document, bool) {
	if _, err := resolveFile(pathReference{root: v.repoRoot, reference: location.target.path}); err != nil {
		v.add(location.target, "document", err.Error())
		return Document{}, false
	}
	doc, err := Load(location.path)
	if err != nil {
		v.add(location.target, "document", "invalid or unknown field: "+err.Error())
		return Document{}, false
	}
	if doc.SchemaVersion != SchemaVersion {
		v.add(location.target, "schema_version", fmt.Sprintf("got %d; want %d", doc.SchemaVersion, SchemaVersion))
	}
	if len(doc.Results) == 0 {
		v.add(location.target, "results", "must contain at least one result")
		return doc, false
	}
	return doc, true
}

func (v *validation) validateResults(context resultFileContext, results []Result) {
	seenProfile := map[string]bool{}
	for i := range results {
		result := &results[i]
		v.validateResult(context, result)
		if result.ExecutionProfile != string(matrix.ProfileDesktopLocal) {
			continue
		}
		if context.canonical && result.ScenarioID == context.canonicalScenario {
			v.resultCount[result.ScenarioID]++
		}
		if seenProfile[result.ExecutionProfile] {
			target := context.target
			target.scenario = result.ScenarioID
			v.add(target, "results", "duplicate desktop-local result")
		}
		seenProfile[result.ExecutionProfile] = true
	}
}

func metadataScenarioID(cellDir string) string {
	b, err := os.ReadFile(filepath.Join(cellDir, metadataFile))
	if err != nil {
		return ""
	}
	var metadata struct {
		ScenarioID string `json:"scenario_id"`
	}
	if json.Unmarshal(b, &metadata) != nil {
		return ""
	}
	return metadata.ScenarioID
}

func (v *validation) validateResult(context resultFileContext, result *Result) {
	scenario := result.ScenarioID
	target := context.target
	target.scenario = scenario
	resultContext := resultValidationContext{target: target, cellDir: context.cellDir, result: result}
	v.validateScenario(scenarioValidation{target: target, expected: context.expectedScenario, actual: scenario})
	profile, err := matrix.ParseExecutionProfile(result.ExecutionProfile)
	if err != nil {
		v.add(target, "execution_profile", err.Error())
	} else if profile != matrix.ProfileDesktopLocal {
		v.add(target, "execution_profile", "result files support only desktop-local")
	}
	if !knownOutcome(result.Outcome) {
		v.add(target, "outcome", fmt.Sprintf("unknown outcome %q", result.Outcome))
		return
	}
	if observedOutcome(result.Outcome) {
		v.validateObservedShape(resultContext)
		return
	}
	v.validateNonObservedShape(resultContext)
}

func (v *validation) validateScenario(input scenarioValidation) {
	if strings.TrimSpace(input.actual) == "" {
		input.target.scenario = input.expected
		v.add(input.target, "scenario_id", "must not be blank")
		return
	}
	if !v.catalog[input.actual] {
		v.add(input.target, "scenario_id", "does not name a current catalog scenario")
	}
	if input.expected == "" {
		v.add(input.target, "scenario_id", "cannot match result to a valid sibling metadata.json")
	} else if input.actual != input.expected {
		v.add(input.target, "scenario_id", fmt.Sprintf("does not match cell metadata scenario_id %q", input.expected))
	}
}

func (v *validation) validateObservedShape(context resultValidationContext) {
	result := context.result
	if !safeSegment(result.Recording) {
		v.add(context.target, "recording", "must be one safe recording directory name")
		return
	}
	if result.Evidence == nil {
		v.add(context.target, "evidence", "is required for an observed result")
		return
	}
	v.validateCanonicalEvidenceNames(context)
	if len(result.EvidenceRefs) > 0 {
		v.add(context.target, "evidence_refs", "is only valid for a non-observed result")
	}
	if strings.TrimSpace(result.MissingControl) != "" {
		v.add(context.target, "missing_control", "is only valid for not-runnable")
	}
	if result.Outcome == OutcomeObservedFailure && strings.TrimSpace(result.Reason) == "" {
		v.add(context.target, "reason", "is required for observed-failure")
	}
	v.validateObservedEvidence(context)
}

func (v *validation) validateCanonicalEvidenceNames(context resultValidationContext) {
	result := context.result
	actual := map[string]string{
		"desktop_registry": result.Evidence.DesktopRegistry,
		"transcript":       result.Evidence.Transcript,
		"hooks":            result.Evidence.Hooks,
		"process":          result.Evidence.Process,
		"irrlicht_session": result.Evidence.IrrlichtSession,
		"environment":      result.Evidence.Environment,
	}
	expected := map[string]string{
		"desktop_registry": DesktopRegistryFile,
		"transcript":       TranscriptFile,
		"hooks":            HooksFile,
		"process":          ProcessFile,
		"irrlicht_session": IrrlichtSessionFile,
		"environment":      EnvironmentFile,
	}
	for field, want := range expected {
		if actual[field] != want {
			v.add(context.target, "evidence."+field, fmt.Sprintf("got %q; want canonical %q", actual[field], want))
		}
	}
}

func (v *validation) validateNonObservedShape(context resultValidationContext) {
	v.validateNonObservedEvidence(context)
	v.validateNonObservedExclusions(context)
}

func (v *validation) validateNonObservedEvidence(context resultValidationContext) {
	result := context.result
	if strings.TrimSpace(result.Reason) == "" {
		v.add(context.target, "reason", "must contain an evidence-based reason")
	}
	if len(result.EvidenceRefs) == 0 {
		v.add(context.target, "evidence_refs", "must contain at least one repository evidence reference")
	}
	for i, ref := range result.EvidenceRefs {
		field := fmt.Sprintf("evidence_refs[%d]", i)
		resolved, err := resolveFile(pathReference{root: v.repoRoot, reference: ref})
		if err != nil {
			v.add(context.target, field, err.Error())
			continue
		}
		if !v.allowedNonObservedEvidence(evidenceBoundary{cellDir: context.cellDir, resolved: resolved}) {
			v.add(context.target, field, "must name same-cell Desktop evidence or an explicit repository Desktop evidence source")
		}
	}
}

// allowedNonObservedEvidence checks only a reference's stable scope. It does
// not claim that prose proves the result. Campaign evidence must be the cell's
// metadata, a file under its desktop-evidence directory, or a shared raw probe
// under replaydata/agents/claudecode/desktop-evidence.
func (v *validation) allowedNonObservedEvidence(boundary evidenceBoundary) bool {
	if sameResolvedFile(pathPair{left: boundary.resolved, right: filepath.Join(boundary.cellDir, metadataFile)}) {
		return true
	}
	for _, root := range []string{
		filepath.Join(boundary.cellDir, "desktop-evidence"),
		filepath.Join(v.repoRoot, "replaydata", "agents", claudeAdapter, "desktop-evidence"),
	} {
		if resolvedWithinExisting(pathPair{left: root, right: boundary.resolved}) {
			return true
		}
	}
	return false
}

func (v *validation) validateNonObservedExclusions(context resultValidationContext) {
	result := context.result
	if result.Recording != "" {
		v.add(context.target, "recording", "is only valid for an observed result")
	}
	if result.Evidence != nil {
		v.add(context.target, "evidence", "is only valid for an observed result")
	}
	missing := strings.TrimSpace(result.MissingControl)
	if result.Outcome == OutcomeNotRunnable && missing == "" {
		v.add(context.target, "missing_control", "must name the unavailable Desktop control")
	}
	if result.Outcome != OutcomeNotRunnable && missing != "" {
		v.add(context.target, "missing_control", "is only valid for not-runnable")
	}
}

func (v *validation) validateCompleteness() {
	v.validateDuplicateResults()
	if v.requireDesktop {
		v.validateRequiredResults()
	}
}

func (v *validation) validateDuplicateResults() {
	for scenario, count := range v.resultCount {
		if count > 1 {
			v.add(findingTarget{path: resultCellsPath(), scenario: scenario}, "completeness", fmt.Sprintf("duplicate desktop-local result: found %d entries", count))
		}
	}
}

func (v *validation) validateRequiredResults() {
	for scenario := range v.claudeCells {
		count := v.resultCount[scenario]
		if count == 0 {
			v.add(findingTarget{path: resultCellsPath(), scenario: scenario}, "completeness", "missing desktop-local result")
		}
	}
}

func resultCellsPath() string {
	return filepath.Join("replaydata", "agents", claudeAdapter, "scenarios")
}

func sameResolvedFile(paths pathPair) bool {
	resolved, err := filepath.EvalSymlinks(paths.right)
	return err == nil && resolved == paths.left
}

func resolvedWithinExisting(paths pathPair) bool {
	resolvedRoot, err := filepath.EvalSymlinks(paths.left)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedRoot, paths.right)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (v *validation) rel(path string) string {
	rel, err := filepath.Rel(v.repoRoot, path)
	if err != nil {
		return path
	}
	return rel
}

func safeSegment(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value
}

type resolvedPath struct {
	path string
	info os.FileInfo
}

// resolveFile accepts only an existing, non-empty regular file below root.
// EvalSymlinks makes a repository-relative reference unable to escape through
// a committed or locally-created symlink.
func resolveFile(input pathReference) (string, error) {
	resolved, err := resolveExisting(input)
	if err != nil {
		return "", err
	}
	if !resolved.info.Mode().IsRegular() || resolved.info.Size() == 0 {
		return "", fmt.Errorf("reference %q must name a non-empty regular file", input.reference)
	}
	return resolved.path, nil
}

func resolveDirectory(input pathReference) (string, error) {
	if !safeSegment(input.reference) {
		return "", fmt.Errorf("must be one safe directory name")
	}
	resolved, err := resolveExisting(input)
	if err != nil {
		return "", fmt.Errorf("cannot resolve recording %q: %w", input.reference, err)
	}
	if !resolved.info.IsDir() {
		return "", fmt.Errorf("recording %q must name a directory", input.reference)
	}
	return resolved.path, nil
}

func resolveExisting(input pathReference) (resolvedPath, error) {
	clean, err := cleanReference(input.reference)
	if err != nil {
		return resolvedPath{}, err
	}
	rootResolved, err := filepath.EvalSymlinks(input.root)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("cannot resolve evidence root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(input.root, clean))
	if err != nil {
		return resolvedPath{}, fmt.Errorf("cannot read %q: %w", input.reference, err)
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("cannot compare evidence root and reference: %w", err)
	}
	if escapesRoot(rel) {
		return resolvedPath{}, fmt.Errorf("reference %q escapes its evidence root", input.reference)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("cannot read %q: %w", input.reference, err)
	}
	return resolvedPath{path: resolved, info: info}, nil
}

func escapesRoot(rel string) bool {
	if rel == ".." {
		return true
	}
	return strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cleanReference(reference string) (string, error) {
	if reference == "" {
		return "", fmt.Errorf("must be a non-empty relative path")
	}
	if filepath.IsAbs(reference) {
		return "", fmt.Errorf("must be a non-empty relative path")
	}
	native := filepath.FromSlash(reference)
	clean := filepath.Clean(native)
	if clean == "." {
		return "", fmt.Errorf("must not traverse outside its evidence root")
	}
	if escapesRoot(clean) {
		return "", fmt.Errorf("must not traverse outside its evidence root")
	}
	if clean != native {
		return "", fmt.Errorf("must not contain traversal or redundant path components")
	}
	return clean, nil
}
