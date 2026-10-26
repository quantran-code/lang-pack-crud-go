package httpapi

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// logFields splits one access log line into its key=value fields. Tokens are separated by spaces
// that sit outside a quoted value, because a value the log had to quote may itself contain a
// space; the existing requireLogFields splits on any space, which cannot parse that. A quoted
// value is unquoted with strconv.Unquote. A token without '=', a malformed or unterminated quoted
// value, or a duplicate key fails the test.
func logFields(t *testing.T, line string) map[string]string {
	t.Helper()

	fields := make(map[string]string)
	remaining := line
	for remaining != "" {
		remaining = strings.TrimLeft(remaining, " ")
		if remaining == "" {
			break
		}

		key, rest, ok := strings.Cut(remaining, "=")
		if !ok {
			t.Fatalf("log line %q: token %q is not key=value", line, remaining)
		}

		var value string
		if strings.HasPrefix(rest, `"`) {
			end := -1
			for i := 1; i < len(rest); i++ {
				if rest[i] == '\\' {
					i++
					continue
				}
				if rest[i] == '"' {
					end = i
					break
				}
			}
			if end < 0 {
				t.Fatalf("log line %q: unterminated quoted value for %q", line, key)
			}
			unquoted, err := strconv.Unquote(rest[:end+1])
			if err != nil {
				t.Fatalf("log line %q: unquoting %q: %v", line, rest[:end+1], err)
			}
			value = unquoted
			rest = rest[end+1:]
			if rest != "" && rest[0] != ' ' {
				t.Fatalf("log line %q: quoted value for %q is followed by %q, want a space", line, key, rest)
			}
		} else {
			end := strings.IndexByte(rest, ' ')
			if end < 0 {
				value, rest = rest, ""
			} else {
				value, rest = rest[:end], rest[end:]
			}
		}

		if _, duplicate := fields[key]; duplicate {
			t.Fatalf("log line %q: duplicate key %q", line, key)
		}
		fields[key] = value
		remaining = rest
	}
	return fields
}

func TestAccessLogEscapesPath(t *testing.T) {
	var captured bytes.Buffer
	router := newTestRouterWithLog(t, &captured)

	// The URL parser decodes %0A into the path, so the raw newline really is in the value the log
	// would write bare and split the line in two.
	rec := doRequest(t, router, http.MethodGet, "/a%0Afake_line", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	lines := capturedLines(&captured)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly 1 (%q)", len(lines), captured.String())
	}
	if want := `path="/a\nfake_line"`; !strings.Contains(lines[0], want) {
		t.Errorf("log line %q does not contain %s", lines[0], want)
	}
}

func TestAccessLogEscapesRequestID(t *testing.T) {
	const incoming = "x status=999 method=FAKE"

	var captured bytes.Buffer
	router := newTestRouterWithLog(t, &captured)

	rec := doRequestWithID(t, router, http.MethodGet, "/health", incoming)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	lines := capturedLines(&captured)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly 1 (%q)", len(lines), captured.String())
	}

	fields := logFields(t, lines[0])
	if len(fields) != 5 {
		t.Errorf("log line %q has %d fields, want exactly 5", lines[0], len(fields))
	}
	if got, want := fields["status"], strconv.Itoa(http.StatusOK); got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := fields["method"], http.MethodGet; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := fields["path"], "/health"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := fields["request_id"], incoming; got != want {
		t.Errorf("request_id = %q, want %q", got, want)
	}
}
