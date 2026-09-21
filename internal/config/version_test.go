package config

import (
	"runtime/debug"
	"strings"
	"testing"
)

// Every shape a build can arrive in, because the suite's own binary is only
// ever one of them and the constant this replaced was wrong precisely in the
// shape nobody was looking at.
func TestVersionOfEveryShapeOfBuild(t *testing.T) {
	stamp := func(rev, modified string) []debug.BuildSetting {
		var s []debug.BuildSetting
		if rev != "" {
			s = append(s, debug.BuildSetting{Key: "vcs.revision", Value: rev})
		}
		if modified != "" {
			s = append(s, debug.BuildSetting{Key: "vcs.modified", Value: modified})
		}
		return s
	}
	for _, tc := range []struct {
		name string
		bi   *debug.BuildInfo
		want string
	}{
		{"go install of a tag", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.2"}}, "v0.1.2"},
		{"a prerelease tag is still a release", &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0-rc1"}}, "v0.2.0-rc1"},
		{"a pseudo-version is what go install gave it", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260921120000-38cc000f1234"}}, "v0.0.0-20260921120000-38cc000f1234"},
		{
			"make install from a clean checkout",
			&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: stamp("38cc000f1234567890", "false")},
			"(devel 38cc000)",
		},
		{
			"make install from a dirty checkout",
			&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: stamp("38cc000f1234567890", "true")},
			"(devel 38cc000 dirty)",
		},
		{
			"an older toolchain leaves Main.Version empty",
			&debug.BuildInfo{Settings: stamp("38cc000f1234567890", "false")},
			"(devel 38cc000)",
		},
		{
			// Captured, not imagined: `go build ./cmd/ptrbox` under go1.27.1
			// in this checkout reported exactly this Main.Version beside
			// those settings. The stamp wins, or ptrbox would announce a
			// release number that was never tagged.
			"a modern toolchain computes a pseudo-version for a checkout build",
			&debug.BuildInfo{
				Main:     debug.Module{Version: "v0.1.2-0.20260921141752-28e1aeb76782+dirty"},
				Settings: stamp("28e1aeb7678279ceb04782c7ac51f37f5695746c", "true"),
			},
			"(devel 28e1aeb dirty)",
		},
		{"a test binary has neither", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "(devel)"},
		{"no build info at all", nil, "(devel)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionOf(tc.bi); got != tc.want {
				t.Errorf("versionOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// The regression guard: this binary is a `go test` build, so its version must
// read as a development one. It fails the day somebody writes a release
// number down in the source again, which is the trap that produced item 86.
func TestTheSuitesOwnBinaryIsNotAReleaseNumber(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version() is empty")
	}
	if !Devel(v) {
		t.Errorf("Version() = %q under `go test`, want a (devel...) build - a release number "+
			"here means it is written down somewhere rather than read from the build", v)
	}
	if strings.ContainsAny(v, " \t\n") && !strings.HasPrefix(v, "(devel ") {
		t.Errorf("Version() = %q: a version is one token or a (devel ...) parenthesis", v)
	}
}

// Version reads the real build info rather than a stub, which is what makes
// the guard above mean anything.
func TestVersionReadsTheBuild(t *testing.T) {
	real := readBuildInfo
	t.Cleanup(func() { readBuildInfo = real })
	readBuildInfo = func() *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}
	}
	if got := Version(); got != "v9.9.9" {
		t.Errorf("Version() = %q, want the build's own version", got)
	}
}
