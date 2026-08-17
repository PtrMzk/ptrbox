package render

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func renderString(t *testing.T, files fstest.MapFS, template, includeDir string, values Values) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := Render(&buf, files, template, includeDir, values)
	return buf.String(), err
}

func file(content string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(content)} }

// --- placeholder substitution ------------------------------------------------

func TestSubstitutesPlaceholders(t *testing.T) {
	files := fstest.MapFS{"t.yaml": file("hello __NAME__, port __PORT__\n")}
	got, err := renderString(t, files, "t.yaml", ".", Values{"NAME": "world", "PORT": "8888"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world, port 8888\n" {
		t.Errorf("got %q", got)
	}
}

func TestSubstitutesValuesContainingSpacesAndCommas(t *testing.T) {
	files := fstest.MapFS{"t.yaml": file("__SET__\n")}
	got, _ := renderString(t, files, "t.yaml", ".", Values{"SET": "9.9.9.9, 1.1.1.1"})
	if got != "9.9.9.9, 1.1.1.1\n" {
		t.Errorf("got %q", got)
	}
}

func TestLeavesUnknownPlaceholdersForTheCallerToCatch(t *testing.T) {
	files := fstest.MapFS{"t.yaml": file("__UNSET__\n")}
	got, _ := renderString(t, files, "t.yaml", ".", Values{"NAME": "world"})
	if got != "__UNSET__\n" {
		t.Errorf("got %q", got)
	}
}

func TestSubstitutionDoesNotRecurse(t *testing.T) {
	// A value that looks like a placeholder is data, not a further
	// substitution: the git identity and the package list come from a config
	// file, and neither may reach a second round of expansion.
	files := fstest.MapFS{"t.yaml": file("name=__GIT_USER_NAME__ dir=__REPO_DIR__\n")}
	got, _ := renderString(t, files, "t.yaml", ".", Values{
		"GIT_USER_NAME": "__REPO_DIR__",
		"REPO_DIR":      "/Users/example/code/demo",
	})
	if got != "name=__REPO_DIR__ dir=/Users/example/code/demo\n" {
		t.Errorf("got %q", got)
	}
}

// --- includes ----------------------------------------------------------------

func TestInlinesAnIncludeAtTheMarkersIndentation(t *testing.T) {
	files := fstest.MapFS{
		"t.yaml": file("a: |\n  __INCLUDE:inc.sh__\n"),
		"inc.sh": file("#!/bin/bash\nset -eux\n"),
	}
	got, err := renderString(t, files, "t.yaml", ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a: |\n  #!/bin/bash\n  set -eux\n" {
		t.Errorf("got %q", got)
	}
}

func TestSubstitutesPlaceholdersInsideIncludedFiles(t *testing.T) {
	files := fstest.MapFS{
		"t.yaml": file("  __INCLUDE:inc.sh__\n"),
		"inc.sh": file("host=__PROXY_HOST__\n"),
	}
	got, _ := renderString(t, files, "t.yaml", ".", Values{"PROXY_HOST": "192.168.5.2"})
	if got != "  host=192.168.5.2\n" {
		t.Errorf("got %q", got)
	}
}

func TestKeepsBlankLinesInIncludesFreeOfTrailingWhitespace(t *testing.T) {
	// A YAML literal block tolerates it, but trailing whitespace makes every
	// future diff noisy.
	files := fstest.MapFS{
		"t.yaml": file("  __INCLUDE:inc.sh__\n"),
		"inc.sh": file("one\n\ntwo\n"),
	}
	got, _ := renderString(t, files, "t.yaml", ".", nil)
	if got != "  one\n\n  two\n" {
		t.Errorf("got %q", got)
	}
}

func TestFailsOnAMissingInclude(t *testing.T) {
	files := fstest.MapFS{"t.yaml": file("  __INCLUDE:nope.sh__\n")}
	_, err := renderString(t, files, "t.yaml", ".", nil)
	if err == nil || !strings.Contains(err.Error(), "included file not found") {
		t.Errorf("err = %v", err)
	}
}

func TestFailsOnNestedIncludes(t *testing.T) {
	files := fstest.MapFS{
		"t.yaml": file("  __INCLUDE:a.sh__\n"),
		"a.sh":   file("__INCLUDE:b.sh__\n"),
		"b.sh":   file("x\n"),
	}
	_, err := renderString(t, files, "t.yaml", ".", nil)
	if err == nil || !strings.Contains(err.Error(), "nested includes") {
		t.Errorf("err = %v", err)
	}
}

func TestFailsOnAMissingTemplate(t *testing.T) {
	_, err := renderString(t, fstest.MapFS{}, "nope.yaml", ".", nil)
	if err == nil {
		t.Error("rendering a missing template succeeded")
	}
}

// --- render to file ----------------------------------------------------------

func TestRenderFileRefusesLeftoverPlaceholders(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.yaml")
	files := fstest.MapFS{"t.yaml": file("a: __GIT_USER_NAME__\n")}
	err := RenderFile(out, files, "t.yaml", ".", nil)
	if err == nil || !strings.Contains(err.Error(), "unsubstituted placeholders") ||
		!strings.Contains(err.Error(), "__GIT_USER_NAME__") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("a rejected render still wrote its output file")
	}
}

func TestRenderFileLeavesNoTempFileBehindOnFailure(t *testing.T) {
	dir := t.TempDir()
	files := fstest.MapFS{"t.yaml": file("  __INCLUDE:nope.sh__\n")}
	if err := RenderFile(filepath.Join(dir, "out.yaml"), files, "t.yaml", ".", nil); err == nil {
		t.Fatal("expected a failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestRenderFileWritesTheOutputWhenEverythingResolves(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.yaml")
	files := fstest.MapFS{"t.yaml": file("a: __NAME__\n")}
	if err := RenderFile(out, files, "t.yaml", ".", Values{"NAME": "ok"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a: ok\n" {
		t.Errorf("got %q", got)
	}
}

func TestRenderFileOverwritesAnExistingOutput(t *testing.T) {
	// `ptrbox new` after a template change re-renders over the old config.
	out := filepath.Join(t.TempDir(), "out.yaml")
	if err := os.WriteFile(out, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"t.yaml": file("fresh\n")}
	if err := RenderFile(out, files, "t.yaml", ".", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "fresh\n" {
		t.Errorf("got %q", got)
	}
}
