package config

import (
	"runtime/debug"
	"strings"
)

// Version is what `ptrbox version` prints: what this binary IS, read from the
// build rather than written down.
//
// It used to be a constant, hand-bumped - and it was not bumped for v0.1.1,
// so the build that cannot see Multipass and the build that can both answered
// "ptrbox 0.1.0". That is the one question this command exists to answer, and
// the cost was real: a Windows install failing with `brew install lima` had
// to be diagnosed with `go version -m`, because the command whose job it is
// could not tell two builds apart. Bumping the constant would have fixed that
// day and re-armed the same trap, since nothing makes anyone remember.
//
// The build knows. A `go install ...@v0.1.2` carries its module version - the
// tag verbatim, so what this prints can be pasted back into `git show` or
// `go install`. A build from a checkout (`make install`, which is how the
// developer's own binary is made) has no module version and could never stamp
// a release number honestly; it has something better, the commit it was built
// from and whether the tree was dirty.
func Version() string { return versionOf(readBuildInfo()) }

// readBuildInfo is a seam: the suite's own binary is only ever one shape, and
// the decision below has to be tested against all of them.
var readBuildInfo = func() *debug.BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return bi
}

// versionOf is the whole decision, separated from where the information comes
// from so every shape a build arrives in is a table row rather than a machine
// somebody has to be standing at.
func versionOf(bi *debug.BuildInfo) string {
	if bi == nil {
		return "(devel)"
	}
	var revision, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	// The VCS stamp is asked FIRST, and that ordering is the decision.
	//
	// A build from a working tree carries vcs.revision; a `go install
	// module@v0.1.2` is built from the module cache and carries none, so the
	// stamp's presence is exactly "this came from a checkout". It has to be
	// asked first because a modern toolchain no longer leaves Main.Version
	// empty there: go1.27 computes a pseudo-version from the same stamp, and
	// `go build ./cmd/ptrbox` on this tree reported
	// v0.1.2-0.20260921141752-28e1aeb76782+dirty - a string that opens with a
	// release number nobody ever tagged, which is the confusion this command
	// exists to remove. Reading the stamp ourselves also keeps the answer
	// short and says `dirty` in a word.
	if revision == "" {
		// No stamp: either a release (below) or a `go test` binary, which is
		// stamped with neither.
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		return "(devel)"
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	out := "(devel " + revision
	// Dirty matters more than the commit: it means the binary contains
	// something no commit holds, so the revision alone would be a lie.
	if modified == "true" {
		out += " dirty"
	}
	return out + ")"
}

// Devel reports whether v is a build from a checkout rather than a release.
// Used by the tests that must not hard-code a release number.
func Devel(v string) bool { return strings.HasPrefix(v, "(devel") }
