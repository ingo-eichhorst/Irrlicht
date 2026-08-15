// architecture_hookbody_shapes_test.go is the corpus behind the rule in
// architecture_hookbody_test.go: one file per way of reading an inbound request
// body, each pinned to the verdict the detector must return for it.
//
// It exists because a guard that passes by construction is only worth what its
// deliberate failures prove, and a scratch mutation proves it once, for the
// author, on the day of the PR. Worse, a mutation harness that silently stops
// mutating produces a perfect green — so every case here asserts the shape it
// planted is actually PRESENT in the source it hands the detector, before
// asserting anything about the verdict (see plantedShapeIsPresent).
//
// The corpus is also the answer to the question the rule cannot answer about
// itself: "does it catch a decoder stored in a variable first? io.ReadAll? a
// helper in another file?" Those are recorded here as executable cases rather
// than as prose in a PR body, so a later rewrite of the detector has to replay
// them instead of re-deriving which shapes its predecessor caught. That is the
// #1450 lesson (a rewritten scan silently lost a shape the original caught,
// because every new probe was an addition and none pinned the old behaviour)
// applied before there is a predecessor to lose.
//
// Two cases pin NON-detection, and they are as load-bearing as the rest. A
// detector that fired on *http.Response.Body would make every HTTP client call
// in an adapter a violation; one that fired on r.Method would fire on all four
// receivers' method checks. Cheap to get wrong, invisible once wrong.
package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// shapeCase is one spelling, and whether the detector must report it.
type shapeCase struct {
	file string
	// want is whether requestBodyReadsIn must report a read in THIS file.
	want bool
	// needle is a substring that must appear in src, asserted before the
	// verdict is checked. If the corpus ever stops containing the construct a
	// case claims to test, that is a failure of the corpus, not a pass of the
	// rule.
	needle string
	why    string
	src    string
}

const shapesInScopePkg = "shapes.test/inscope"
const shapesOutOfScopePkg = "shapes.test/outofscope"

// inScopeShapes are compiled as one package and fed to the detector. Each is a
// complete file in package inscope.
func inScopeShapes() []shapeCase {
	return []shapeCase{
		{
			file:   "direct_decoder.go",
			want:   true,
			needle: "json.NewDecoder(r.Body).Decode",
			why:    "the spelling all four receivers used before #1389",
			src: `package inscope

import (
	"encoding/json"
	"net/http"
)

type directPayload struct {
	TranscriptPath string ` + "`json:\"transcript_path\"`" + `
}

func ServeDirect(w http.ResponseWriter, r *http.Request) {
	var p directPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		return
	}
	_ = p
}
`,
		},
		{
			file:   "decoder_in_variable.go",
			want:   true,
			needle: "dec := json.NewDecoder(r.Body)",
			why:    "the decoder is built on one line and used on another; a rule matching the literal call expression json.NewDecoder(r.Body).Decode would miss it",
			src: `package inscope

import (
	"encoding/json"
	"net/http"
)

func ServeDecoderInVariable(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var p map[string]string
	if err := dec.Decode(&p); err != nil {
		return
	}
}
`,
		},
		{
			file:   "readall_unmarshal.go",
			want:   true,
			needle: "io.ReadAll(r.Body)",
			why:    "no json.NewDecoder anywhere; the body is slurped and unmarshalled",
			src: `package inscope

import (
	"encoding/json"
	"io"
	"net/http"
)

func ServeReadAll(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	var p map[string]string
	_ = json.Unmarshal(b, &p)
}
`,
		},
		{
			file:   "body_aliased.go",
			want:   true,
			needle: "body := r.Body",
			why:    "the body is aliased into a local before anything touches it",
			src: `package inscope

import (
	"encoding/json"
	"net/http"
)

func ServeAliased(w http.ResponseWriter, r *http.Request) {
	body := r.Body
	defer func() { _ = body.Close() }()
	var p map[string]string
	_ = json.NewDecoder(body).Decode(&p)
}
`,
		},
		{
			file:   "helper_caller.go",
			want:   false,
			needle: "readBodyHelper(r)",
			why:    "the CALLER passes the whole *http.Request onward and never names .Body, so the read is reported at the helper (helper_impl.go) and not here — the rule is per-tree, not per-file, so this is caught, just not attributed here",
			src: `package inscope

import "net/http"

func ServeViaHelper(w http.ResponseWriter, r *http.Request) {
	p := readBodyHelper(r)
	_ = p
}
`,
		},
		{
			file:   "helper_impl.go",
			want:   true,
			needle: "json.NewDecoder(req.Body)",
			why:    "a helper in ANOTHER FILE of the same package, whose parameter is not even called r — proves the detector is type-driven, not name-driven",
			src: `package inscope

import (
	"encoding/json"
	"net/http"
)

func readBodyHelper(req *http.Request) map[string]string {
	var p map[string]string
	_ = json.NewDecoder(req.Body).Decode(&p)
	return p
}
`,
		},
		{
			file:   "helper_takes_reader.go",
			want:   true,
			needle: "decodeFromReader(r.Body)",
			why:    "the helper takes an io.Reader, so the body escapes at the CALL SITE; the selector is right here even though no decode is",
			src: `package inscope

import (
	"encoding/json"
	"io"
	"net/http"
)

func decodeFromReader(src io.Reader) map[string]string {
	var p map[string]string
	_ = json.NewDecoder(src).Decode(&p)
	return p
}

func ServeViaReaderHelper(w http.ResponseWriter, r *http.Request) {
	_ = decodeFromReader(r.Body)
}
`,
		},
		{
			file:   "form_value.go",
			want:   true,
			needle: `r.FormValue("transcript_path")`,
			why:    "reads the body without ever naming Body; the same untrusted path through a different door",
			src: `package inscope

import "net/http"

func ServeFormValue(w http.ResponseWriter, r *http.Request) {
	path := r.FormValue("transcript_path")
	_ = path
}
`,
		},
		{
			file:   "parse_multipart.go",
			want:   true,
			needle: "r.ParseMultipartForm(",
			why:    "consumes the body directly; same reasoning as FormValue",
			src: `package inscope

import "net/http"

func ServeMultipart(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(1 << 20)
}
`,
		},
		{
			file:   "struct_field_request.go",
			want:   true,
			needle: "s.req.Body",
			why:    "the request is stashed in a struct field first, so the selector base is not an identifier of request type but an expression of it",
			src: `package inscope

import (
	"encoding/json"
	"net/http"
)

type requestHolder struct {
	req *http.Request
}

func (s *requestHolder) Decode() map[string]string {
	var p map[string]string
	_ = json.NewDecoder(s.req.Body).Decode(&p)
	return p
}
`,
		},
		{
			file:   "embedded_request.go",
			want:   true,
			needle: "struct {\n\t*http.Request",
			why: "a request-wrapper struct EMBEDDING *http.Request, so Body and FormValue are promoted: " +
				"the selector base types as the wrapper, not as the request. Found by review of #1389 — " +
				"the detector's first cut typed sel.X and missed both, which is a bypass INSIDE the governed " +
				"tree (a receiver written as `type hookCtx struct{ *http.Request }` would decode its own body " +
				"and keep the build green)",
			src: `package inscope

import (
	"encoding/json"
	"net/http"
)

type embeddedCtx struct {
	*http.Request
	extra string
}

func ServeEmbeddedField(w http.ResponseWriter, r *http.Request) map[string]string {
	c := embeddedCtx{Request: r}
	var p map[string]string
	_ = json.NewDecoder(c.Body).Decode(&p)
	return p
}

func ServeEmbeddedMethod(w http.ResponseWriter, r *http.Request) string {
	c := embeddedCtx{Request: r}
	return c.FormValue("transcript_path")
}
`,
		},
		{
			file:   "escape_via_outside_helper.go",
			want:   false,
			needle: "outofscope.ReadBody(r)",
			why:    "THE DOCUMENTED GAP: the whole *http.Request is handed to a package outside the governed tree, which reads .Body there. Nothing in scope names .Body, so the rule cannot see it",
			src: `package inscope

import (
	"net/http"

	"shapes.test/outofscope"
)

func ServeViaOutsideHelper(w http.ResponseWriter, r *http.Request) {
	_ = outofscope.ReadBody(r)
}
`,
		},
		{
			file:   "response_body.go",
			want:   false,
			needle: "resp.Body",
			why:    "MUST NOT FIRE: an *http.Response body is an outbound client read. A name-based rule would fire here and make every HTTP client call in an adapter a violation",
			src: `package inscope

import (
	"encoding/json"
	"net/http"
)

func FetchUpstream(c *http.Client, url string) map[string]string {
	resp, err := c.Get(url)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	var p map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&p)
	return p
}
`,
		},
		{
			file:   "request_metadata.go",
			want:   false,
			needle: "r.Method",
			why:    "MUST NOT FIRE: reading method, URL and headers is not reading the body, and all four real receivers do it on their first line",
			src: `package inscope

import "net/http"

func ServeMetadataOnly(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.URL.Path
	_ = r.Header.Get("Content-Type")
	w.WriteHeader(http.StatusOK)
}
`,
		},
	}
}

// outOfScopeSrc is the package the escape case reaches into. The detector DOES
// report the read here — what excludes it is the rule's pattern, not blindness —
// and TestRequestBodyReadDetectorCatchesEveryKnownShape asserts exactly that, so
// the gap is recorded as a scope decision rather than a detector limitation.
const outOfScopeSrc = `package outofscope

import (
	"encoding/json"
	"net/http"
)

func ReadBody(r *http.Request) map[string]string {
	var p map[string]string
	_ = json.NewDecoder(r.Body).Decode(&p)
	return p
}
`

func TestRequestBodyReadDetectorCatchesEveryKnownShape(t *testing.T) {
	shapes := inScopeShapes()
	dir := writeShapeCorpus(t, shapes)

	pkgs := loadShapeCorpus(t, dir)

	// Reads found in the governed package, indexed by the file they are in.
	hits := map[string]int{}
	outOfScopeHits := 0
	for _, pkg := range pkgs {
		reads := requestBodyReadsIn(pkg.Fset, pkg.Syntax, pkg.TypesInfo, pkg.PkgPath)
		switch pkg.PkgPath {
		case shapesInScopePkg:
			for _, read := range reads {
				hits[filepath.Base(read.File)]++
			}
		case shapesOutOfScopePkg:
			outOfScopeHits += len(reads)
		default:
			t.Fatalf("unexpected package in corpus: %q", pkg.PkgPath)
		}
	}

	for _, shape := range shapes {
		got := hits[shape.file] > 0
		if got == shape.want {
			continue
		}
		verb := "was NOT reported"
		if got {
			verb = "WAS reported"
		}
		t.Errorf("shape %s %s but want reported=%v\n\twhy this case exists: %s",
			shape.file, verb, shape.want, shape.why)
	}

	// The escape case is a scope decision, not a hole in the detector: prove the
	// same read IS seen when the package is in range. Without this the gap and a
	// broken detector look identical.
	if outOfScopeHits != 1 {
		t.Errorf("out-of-scope helper: detector found %d body read(s), want 1 — "+
			"the escape_via_outside_helper case is meant to show the read is EXCLUDED BY SCOPE, "+
			"not that the detector cannot see it", outOfScopeHits)
	}
}

// writeShapeCorpus materializes the corpus as a self-contained module and
// asserts every planted shape actually landed in the file.
func writeShapeCorpus(t *testing.T, shapes []shapeCase) string {
	t.Helper()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module shapes.test\n\ngo 1.25\n")

	inScope := filepath.Join(dir, "inscope")
	outScope := filepath.Join(dir, "outofscope")
	for _, d := range []string{inScope, outScope} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	mustWrite(t, filepath.Join(outScope, "helper.go"), outOfScopeSrc)

	seen := map[string]bool{}
	for _, shape := range shapes {
		if seen[shape.file] {
			t.Fatalf("duplicate shape file %q — one case would silently overwrite the other", shape.file)
		}
		seen[shape.file] = true
		plantedShapeIsPresent(t, shape)
		mustWrite(t, filepath.Join(inScope, shape.file), shape.src)
	}
	return dir
}

// plantedShapeIsPresent is the guard against a corpus that has quietly stopped
// containing what it claims to. A case whose needle is missing is a case that
// proves nothing, and it would otherwise read as a pass for every want:false
// row and as a detector bug for every want:true one.
func plantedShapeIsPresent(t *testing.T, shape shapeCase) {
	t.Helper()
	assertPlantedShapePresent(t, shape.file, shape.src, shape.needle)
}

// assertPlantedShapePresent is the corpus-agnostic half, shared with
// architecture_shellout_shapes_test.go (#1543). Both corpora are in package
// core_test, so this is one fact rather than two — and the shellout corpus's
// first draft re-inlined only the "contains" half, so a row that declared no
// needle at all passed silently, which is the failure this guard exists to
// prevent happening inside the guard.
func assertPlantedShapePresent(t *testing.T, id, src, needle string) {
	t.Helper()
	if needle == "" {
		t.Fatalf("shape %s declares no needle; every case must assert the construct it plants is present", id)
	}
	if !strings.Contains(src, needle) {
		t.Fatalf("shape %s does not contain its own needle %q — the case is not testing what it says it tests",
			id, needle)
	}
}

func loadShapeCorpus(t *testing.T, dir string) []*packages.Package {
	t.Helper()

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Dir: dir,
		// GOWORK=off: the corpus is a standalone module in a temp dir, and an
		// inherited workspace would try to resolve it against this repo's.
		Env: append(os.Environ(), "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("packages.Load(corpus): %v", err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("corpus does not build (%d error(s)) — a corpus that fails to type-check "+
			"yields no TypesInfo and every want:true case would fail for the wrong reason", n)
	}
	if len(pkgs) != 2 {
		t.Fatalf("corpus loaded %d packages, want 2 (inscope + outofscope)", len(pkgs))
	}
	return pkgs
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
