package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// The config file used to be sourced by bash, which meant a stray line in it
// ran as code with your privileges before ptrbox did anything. It is parsed
// now: KEY=value, comments, and the quoting rules a person writing a shell-ish
// config file would expect, with $VAR expansion so
// `PTRBOX_REPO_ROOT="$HOME/code"` keeps working. Anything that is not an
// assignment is an error rather than something silently skipped - a config
// file that does not parse should say so, not half-apply.

// parseFile reads path and returns the PTRBOX_* assignments it contains,
// keyed without the prefix. A missing file is not an error: no config file is
// the normal state.
func parseFile(path string) (map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	known := make(map[string]bool, len(Keys))
	for _, key := range Keys {
		known[key] = true
	}

	// Assignments are visible to later lines' $VAR expansion, as they would be
	// in a sourced file. Non-PTRBOX_ names are kept for exactly that reason:
	// they are useless as settings but usable as scratch variables.
	locals := map[string]string{}
	values := map[string]string{}
	var warnings []string

	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		name, value, ok, err := parseAssignment(scanner.Text(), func(v string) string {
			if local, ok := locals[v]; ok {
				return local
			}
			return os.Getenv(v)
		})
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if !ok {
			continue // blank or comment
		}
		locals[name] = value
		key, isPtrbox := strings.CutPrefix(name, "PTRBOX_")
		switch {
		case !isPtrbox:
			// Deliberately quiet: a helper variable is a legitimate thing to
			// put in a config file.
		case known[key]:
			values[key] = value
		default:
			warnings = append(warnings,
				fmt.Sprintf("%s:%d: unknown setting %s (ignored)", path, line, name))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return values, warnings, nil
}

// parseAssignment reads one line. ok is false for blank and comment lines.
func parseAssignment(line string, lookup func(string) string) (name, value string, ok bool, err error) {
	rest := strings.TrimLeft(line, " \t")
	if rest == "" || strings.HasPrefix(rest, "#") {
		return "", "", false, nil
	}
	// `export KEY=value` is what half the shell configs in the world look
	// like, and it meant the same thing when this file was sourced.
	if after, found := strings.CutPrefix(rest, "export "); found {
		rest = strings.TrimLeft(after, " \t")
	}

	eq := strings.IndexByte(rest, '=')
	if eq <= 0 || !isName(rest[:eq]) {
		return "", "", false, fmt.Errorf("not a KEY=value assignment: %q", strings.TrimSpace(line))
	}
	name = rest[:eq]

	value, trailing, err := parseValue(rest[eq+1:], lookup)
	if err != nil {
		return "", "", false, err
	}
	trailing = strings.TrimLeft(trailing, " \t")
	if trailing != "" && !strings.HasPrefix(trailing, "#") {
		return "", "", false, fmt.Errorf("trailing text after the value of %s: %q", name, trailing)
	}
	return name, value, true, nil
}

func isName(s string) bool {
	for i, r := range s {
		alpha := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := r >= '0' && r <= '9'
		if !alpha && !(digit && i > 0) {
			return false
		}
	}
	return true
}

// parseValue consumes one shell-ish value and returns it along with whatever
// followed it on the line.
func parseValue(s string, lookup func(string) string) (value, trailing string, err error) {
	switch {
	case strings.HasPrefix(s, "'"):
		// Single quotes are literal, all the way to the closing quote.
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", "", errors.New("unterminated single quote")
		}
		return s[1 : 1+end], s[end+2:], nil

	case strings.HasPrefix(s, `"`):
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				if i+1 < len(s) {
					i++
					b.WriteByte(s[i])
				}
			case '"':
				value, err := expand(b.String(), lookup)
				return value, s[i+1:], err
			default:
				b.WriteByte(s[i])
			}
		}
		return "", "", errors.New("unterminated double quote")

	default:
		// Unquoted: ends at whitespace, as it would in shell. A value with a
		// space in it therefore has to be quoted - which is also the only way
		// it ever worked when this file was sourced.
		end := strings.IndexAny(s, " \t")
		if end < 0 {
			end = len(s)
		}
		word := s[:end]
		if strings.HasPrefix(word, "~/") {
			word = os.Getenv("HOME") + word[1:]
		}
		value, err := expand(word, lookup)
		return value, s[end:], err
	}
}

// expand substitutes $NAME and ${NAME}. A backslash escapes the dollar.
//
// Command substitution is refused rather than left as a literal: somebody
// writing $(nproc) means it to run, and quietly storing the seven characters
// would surface as an incomprehensible validation error three steps later.
func expand(s string, lookup func(string) string) (string, error) {
	if strings.Contains(s, "`") {
		return "", errors.New("command substitution is not supported: the config file is parsed, not executed")
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		rest := s[i+1:]
		if strings.HasPrefix(rest, "(") {
			return "", errors.New("command substitution is not supported: the config file is parsed, not executed")
		}
		if strings.HasPrefix(rest, "{") {
			end := strings.IndexByte(rest, '}')
			if end < 0 {
				b.WriteByte(s[i])
				continue
			}
			b.WriteString(lookup(rest[1:end]))
			i += end + 1
			continue
		}
		end := 0
		for end < len(rest) && isNameByte(rest[end], end) {
			end++
		}
		if end == 0 {
			b.WriteByte(s[i])
			continue
		}
		b.WriteString(lookup(rest[:end]))
		i += end
	}
	return b.String(), nil
}

func isNameByte(c byte, offset int) bool {
	switch {
	case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return offset > 0
	}
	return false
}
