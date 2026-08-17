// The golden files are the review surface for changes to what a VM contains:
// they are the fully rendered configs, provision scripts inlined, exactly as
// Lima receives them. After changing anything under vm/, run `make golden` and
// READ THE DIFF.
//
// External test package: rendertest imports render, so the fixtures cannot be
// used from inside package render itself.
package render_test

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/PtrMzk/ptrbox/internal/rendertest"
)

var update = flag.Bool("update", false, "rewrite the golden files instead of comparing")

func TestGolden(t *testing.T) {
	for _, tc := range []struct {
		name, golden string
		render       func(*testing.T) string
	}{
		{"sandbox", "../../tests/golden/claude-repo.rendered.yaml", rendertest.Sandbox},
		{"proxy", "../../tests/golden/proxy.rendered.yaml", rendertest.Proxy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render(t)
			if *update {
				if err := os.WriteFile(tc.golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("regenerated %s", tc.golden)
				return
			}
			want, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("%s does not match the rendered template.\n"+
					"If the change is intentional: make golden, then review the diff.\n%s",
					tc.golden, diff(string(want), got))
			}
		})
	}
}

// diff reports the first differing line, which is all a reader needs to decide
// whether to run `make golden` or fix the template.
func diff(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, g := at(wantLines, i), at(gotLines, i)
		if w != g {
			return fmt.Sprintf("first difference at line %d:\n  golden: %s\n  render: %s", i+1, w, g)
		}
	}
	return "(files differ only in trailing bytes)"
}

func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(end of file)"
}
