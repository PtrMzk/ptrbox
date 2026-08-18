package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A VM whose mount holds the code that provisions VMs is an agent authoring
// its own sandbox - and that code later runs on the host with your
// privileges. `ptrbox new` therefore refuses any directory that contains a
// ptrbox checkout.
//
// This is a backstop, not the control. The control is reviewing diffs on the
// host before running them; see the trust posture in CLAUDE.md. The binary
// carries its assets embedded, so a checkout is no longer needed at runtime -
// which means the check can no longer just look at "where am I installed
// from" and instead looks for the source itself.

// checkoutMarkers are the files that identify a ptrbox source tree. go.mod is
// the current shape; vm/claude-repo.yaml identifies one from any era,
// including a pre-Go checkout somebody still has lying around.
func isCheckout(dir string) bool {
	if body, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		if strings.Contains(string(body), "module github.com/PtrMzk/ptrbox") {
			return true
		}
	}
	_, tmpl := os.Stat(filepath.Join(dir, "vm", "claude-repo.yaml"))
	_, verify := os.Stat(filepath.Join(dir, "vm", "verify.sh"))
	return tmpl == nil && verify == nil
}

// findCheckoutAbove walks up from start looking for a checkout.
func findCheckoutAbove(start string) string {
	dir := start
	for {
		if isCheckout(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// scanDepth bounds the descendant search. Two levels finds the realistic
// case - pointing at a directory that has a ptrbox checkout in it - without
// turning `ptrbox new ~` into a full-disk walk.
const scanDepth = 2

// skipDirs are directories that cannot hold a ptrbox checkout but can hold
// tens of thousands of subdirectories. Descending them would make `new` on an
// established project pause for no reason.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true,
	"__pycache__": true, "venv": true,
}

// checkoutInside returns the path of a ptrbox checkout at or under dir, or "".
func checkoutInside(dir string) string {
	if isCheckout(dir) {
		return dir
	}
	found := ""
	root := os.DirFS(dir)
	fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return nil //nolint:nilerr // an unreadable subtree is not a reason to fail
		}
		if !d.IsDir() {
			return nil
		}
		// Skip dot directories: .git holds no checkout, and neither do the
		// caches that make a deep scan expensive.
		if strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] {
			return fs.SkipDir
		}
		if isCheckout(filepath.Join(dir, path)) {
			found = filepath.Join(dir, path)
			return fs.SkipAll
		}
		// depth of "a" is 1, of "a/b" is 2 - so at scanDepth we examine the
		// directory but do not descend past it.
		if strings.Count(path, "/")+1 >= scanDepth {
			return fs.SkipDir
		}
		return nil
	})
	return found
}

// containedCheckout reports a ptrbox checkout that mounting repoDir would
// expose, or "" if there is none. It looks in two places: inside repoDir
// itself, and above the places ptrbox is being run from - which is what
// catches `ptrbox new ~` when the checkout is somewhere under it.
func containedCheckout(repoDir, exe string) string {
	if found := checkoutInside(repoDir); found != "" {
		return found
	}
	var starts []string
	if exe != "" {
		starts = append(starts, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	for _, start := range starts {
		root := findCheckoutAbove(start)
		if root == "" {
			continue
		}
		// Resolved before comparing: repoDir is physical, and on macOS /var,
		// /tmp and /etc are symlinks, so the same directory has two spellings
		// and only one of them would match.
		if physical, err := filepath.EvalSymlinks(root); err == nil {
			root = physical
		}
		if isInside(repoDir, root) {
			return root
		}
	}
	return ""
}

// isInside reports whether path is dir or below it. Both are expected to be
// cleaned absolute paths.
func isInside(dir, path string) bool {
	if dir == path {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}
