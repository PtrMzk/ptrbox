package guestfake

import (
	"fmt"
	"regexp"
	"strings"
)

// Log is what a fake backend was asked to do, in order: the record the
// lifecycle tests assert against. It is the same for every backend's fake -
// one line per invocation of the backend's binary - so the assertions read
// the same whichever one a test ran on.
type Log struct {
	// Calls is one line per invocation, in order, with long or multi-line
	// arguments replaced by a <script:N> placeholder so the log stays
	// greppable. Scripts holds what those placeholders stand for.
	Calls   []string
	Scripts []string
	// Stdins is everything a guest was sent on stdin, in order.
	Stdins []string
}

// Record appends one invocation.
func (l *Log) Record(args []string) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if strings.Contains(a, "\n") || len(a) > 60 {
			l.Scripts = append(l.Scripts, a)
			parts = append(parts, fmt.Sprintf("<script:%d>", len(l.Scripts)))
			continue
		}
		parts = append(parts, a)
	}
	l.Calls = append(l.Calls, strings.Join(parts, " "))
}

// Called reports whether any invocation matches the regular expression.
func (l *Log) Called(pattern string) bool { return l.callIndex(pattern) >= 0 }

// callIndex is the position of the first invocation matching pattern, or -1.
func (l *Log) callIndex(pattern string) int {
	re := regexp.MustCompile(pattern)
	for i, call := range l.Calls {
		if re.MatchString(call) {
			return i
		}
	}
	return -1
}

// InOrder reports whether the first call matching first precedes the first
// call matching second. Used to assert orderings that matter: validate before
// start, the proxy up before a sandbox, a candidate parsed before it is moved
// into place.
func (l *Log) InOrder(first, second string) bool {
	i, j := l.callIndex(first), l.callIndex(second)
	return i >= 0 && j >= 0 && i < j
}

// CallLog is the recorded invocations as one string, for failure messages.
func (l *Log) CallLog() string { return strings.Join(l.Calls, "\n") }

// Reset clears the log, keeping VM and filesystem state - the equivalent of
// starting a fresh assertion window mid-scenario.
func (l *Log) Reset() { l.Calls, l.Scripts, l.Stdins = nil, nil, nil }
