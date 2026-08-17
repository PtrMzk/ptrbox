// Package render turns the VM templates into the configs Lima is actually
// started with.
//
// Two substitutions, applied to the template AND to every included file:
//
//	__KEY__                      replaced by the value of KEY
//	__INCLUDE:provision/x.sh__   replaced by that file's contents, with every
//	                             line prefixed by the marker's own indentation
//
// The include mechanism is what lets provision logic live in real .sh files
// (lintable, testable) while the generated Lima config stays self-describing:
// you can read exactly what a VM will run. It also avoids depending on any
// Lima-version-specific way of referencing external scripts.
//
// Indentation matters: a YAML literal block (`script: |`) strips the block's
// common indentation before handing the string to the shell, so uniformly
// prefixed lines arrive at the guest un-indented - which is what keeps heredoc
// terminators inside the provision scripts valid.
package render

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Values are the __KEY__ substitutions for one render.
type Values map[string]string

// placeholderRe matches a __KEY__ token. The colon in an __INCLUDE:...__
// marker keeps it out of this pattern, which is why includes can be handled
// separately without the two mechanisms colliding.
var placeholderRe = regexp.MustCompile(`__[A-Z][A-Z0-9_]*__`)

const includeMarker = "__INCLUDE:"

// Render writes the rendered template to w. Templates and includes are read
// from fsys; includeDir is the directory an __INCLUDE:path__ is relative to.
func Render(w io.Writer, fsys fs.FS, template, includeDir string, values Values) error {
	body, err := fs.ReadFile(fsys, template)
	if err != nil {
		return fmt.Errorf("render: no such template: %s", template)
	}

	out := bufio.NewWriter(w)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		include, indent, ok := parseInclude(line)
		if !ok {
			fmt.Fprintln(out, substitute(line, values))
			continue
		}
		if err := writeInclude(out, fsys, path.Join(includeDir, include), indent, values, template); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("render: %s: %w", template, err)
	}
	return out.Flush()
}

// parseInclude reports whether line is an include marker, and with what
// indentation. Only spaces count as indentation: a YAML literal block is
// space-indented, and a tab here would produce a config Lima rejects for
// reasons that take an afternoon to find.
func parseInclude(line string) (include, indent string, ok bool) {
	start := strings.Index(line, includeMarker)
	if start < 0 {
		return "", "", false
	}
	rest := line[start+len(includeMarker):]
	end := strings.Index(rest, "__")
	if end < 0 {
		return "", "", false
	}
	return rest[:end], line[:len(line)-len(strings.TrimLeft(line, " "))], true
}

func writeInclude(out *bufio.Writer, fsys fs.FS, incPath, indent string, values Values, template string) error {
	body, err := fs.ReadFile(fsys, incPath)
	if err != nil {
		return fmt.Errorf("render: %s: included file not found: %s", template, incPath)
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if _, _, ok := parseInclude(line); ok {
			return fmt.Errorf("render: %s: nested includes are not supported", incPath)
		}
		// Keep blank lines blank rather than emitting trailing whitespace.
		if line == "" {
			fmt.Fprintln(out)
			continue
		}
		fmt.Fprint(out, indent)
		fmt.Fprintln(out, substitute(line, values))
	}
	return scanner.Err()
}

// substitute replaces every known __KEY__ in one pass. Unknown placeholders
// are left alone for RenderFile to catch.
//
// One pass, not one pass per key: sequential replacement would let a value
// that happens to contain __SOME_OTHER_KEY__ be expanded again. No real value
// does, but the git identity and the extra-package list come from a config
// file, and a substitution engine that can be talked into a second round is a
// worse thing to own than a slightly different loop.
func substitute(line string, values Values) string {
	return placeholderRe.ReplaceAllStringFunc(line, func(token string) string {
		if value, ok := values[strings.Trim(token, "_")]; ok {
			return value
		}
		return token
	})
}

// RenderFile renders to a file, refusing to leave a half-rendered artifact
// behind.
//
// Fails if any __PLACEHOLDER__ survived - that means a typo in the template or
// a config key nobody passed, and shipping it would produce a VM that
// misbehaves in a much more confusing way.
func RenderFile(out string, fsys fs.FS, template, includeDir string, values Values) error {
	var buf bytes.Buffer
	if err := Render(&buf, fsys, template, includeDir, values); err != nil {
		return err
	}

	if leftovers := placeholderRe.FindAllString(buf.String(), -1); len(leftovers) > 0 {
		seen := map[string]bool{}
		var unique []string
		for _, l := range leftovers {
			if !seen[l] {
				seen[l] = true
				unique = append(unique, l)
			}
		}
		sort.Strings(unique)
		return fmt.Errorf("render: unsubstituted placeholders: %s", strings.Join(unique, " "))
	}

	// Written whole and moved into place: a reader of ~/.lima/_generated must
	// never find a partial config there, and neither must limactl.
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
