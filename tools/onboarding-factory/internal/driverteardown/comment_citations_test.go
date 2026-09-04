package driverteardown

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var crossFileShellLine = regexp.MustCompile(
	`[A-Za-z0-9_./-]+\.sh:[0-9]+|[A-Za-z0-9_./-]+\.sh[^\n]*\n[^\n]*:[0-9]+`,
)

// TestPackageCommentsDoNotUseCrossFileLineNumbers keeps explanations tied to
// named shell constructs and reproducible searches. Recording drivers and
// their callers change often, so a copied line number becomes false without
// changing the code it describes.
func TestPackageCommentsDoNotUseCrossFileLineNumbers(t *testing.T) {
	t.Helper()
	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("discover Go files: %v", err)
	}
	if len(goFiles) == 0 {
		t.Fatal("no Go files discovered; the comment guard scanned nothing")
	}

	comments := 0
	fset := token.NewFileSet()
	for _, path := range goFiles {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse comments in %s: %v", path, err)
		}
		for _, group := range file.Comments {
			comments++
			assertNoCrossFileShellLine(t, path, group.Text())
		}
	}

	shellFiles, err := filepath.Glob(filepath.Join("testdata", "*.sh"))
	if err != nil {
		t.Fatalf("discover fixture shell files: %v", err)
	}
	if len(shellFiles) == 0 {
		t.Fatal("no fixture shell files discovered; the comment guard scanned no shell fixtures")
	}
	for _, path := range shellFiles {
		comments += checkShellCommentBlocks(t, path)
	}
	if comments == 0 {
		t.Fatal("no comments discovered; the comment guard asserted nothing")
	}
}

func checkShellCommentBlocks(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- paths come from the package fixture glob
	if err != nil {
		t.Fatalf("open shell fixture %s: %v", path, err)
	}
	defer f.Close()

	count := 0
	var block strings.Builder
	flush := func() {
		if block.Len() == 0 {
			return
		}
		count++
		assertNoCrossFileShellLine(t, path, block.String())
		block.Reset()
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			block.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "#")))
			block.WriteByte('\n')
			continue
		}
		if comment, ok := shellComment(line); ok {
			count++
			assertNoCrossFileShellLine(t, path, comment)
		}
		flush()
	}
	flush()
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan shell fixture %s: %v", path, err)
	}
	return count
}

// shellComment returns an inline comment using the same word-boundary rules as
// shellWordsOpen. Quoted hashes and hashes inside expansions are shell data.
func shellComment(line string) (string, bool) {
	wordOpen := false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs):
			wordOpen = true
			i++
		case c == '#' && !wordOpen:
			return string(rs[i+1:]), true
		case c == ' ' || c == '\t':
			wordOpen = false
		case c == '\'' || c == '"':
			j, _ := scanQuoted(rs, i)
			wordOpen = true
			i = j
		case opensExpansion(rs, i):
			wordOpen = true
			i = scanExpansion(rs, i)
		case c == ';' || c == '|' || c == '&' || c == '(' || c == ')':
			wordOpen = false
		default:
			wordOpen = true
		}
	}
	return "", false
}

func TestShellCommentUsesShellWordBoundaries(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "space boundary", line: "value=1 # run-cell.sh:443", want: " run-cell.sh:443", ok: true},
		{name: "separator boundary", line: "value=1;# run-cell.sh:443", want: " run-cell.sh:443", ok: true},
		{name: "open parenthesis boundary", line: "values=(# run-cell.sh:443", want: " run-cell.sh:443", ok: true},
		{name: "close parenthesis boundary", line: "value=1)# run-cell.sh:443", want: " run-cell.sh:443", ok: true},
		{name: "single quoted data", line: "value='# run-cell.sh:443'"},
		{name: "double quoted data", line: `value="# run-cell.sh:443"`},
		{name: "parameter operator", line: "value=${source#run-cell.sh:443}"},
		{name: "escaped hash", line: `value=\#run-cell.sh:443`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := shellComment(test.line)
			if got != test.want || ok != test.ok {
				t.Fatalf("shellComment(%q) = %q, %v; want %q, %v", test.line, got, ok, test.want, test.ok)
			}
		})
	}
}

func assertNoCrossFileShellLine(t *testing.T, path, comment string) {
	t.Helper()
	for _, match := range crossFileShellLine.FindAllString(comment, -1) {
		t.Errorf("%s comment has a hard-coded cross-file shell line %q; name the construct and "+
			"the git grep command that finds it", path, match)
	}
}
