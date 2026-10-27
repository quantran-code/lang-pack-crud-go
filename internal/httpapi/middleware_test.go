package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/quantran-code/lang-pack-crud-go/internal/database"
)

// doRequestWithID drives the real router with a request that carries reqID in the RequestIDHeader,
// so a test can cover both the generated and the reused request ID paths. It sends the shared admin
// token as well, so a protected route answers its normal status.
func doRequestWithID(t *testing.T, router *gin.Engine, method, path, reqID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", authorizationHeader(testAdminToken()))
	if reqID != "" {
		req.Header.Set(RequestIDHeader, reqID)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// capturedLines returns the lines written to buf, without the empty piece a trailing newline
// leaves behind.
func capturedLines(buf *bytes.Buffer) []string {
	text := buf.String()
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// requireLogFields splits one captured logfmt line into its fields and asserts the line carries
// exactly the five agreed keys, each with a non-empty value.
func requireLogFields(t *testing.T, line string) map[string]string {
	t.Helper()

	fields := make(map[string]string, 5)
	for _, field := range strings.Fields(line) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			t.Fatalf("log line %q: field %q is not key=value", line, field)
		}
		if _, duplicate := fields[key]; duplicate {
			t.Fatalf("log line %q: duplicate key %q", line, key)
		}
		fields[key] = value
	}

	wantKeys := []string{"method", "path", "status", "duration", "request_id"}
	if len(fields) != len(wantKeys) {
		t.Fatalf("log line %q: got %d fields, want exactly %d (%v)", line, len(fields), len(wantKeys), wantKeys)
	}
	for _, key := range wantKeys {
		value, ok := fields[key]
		if !ok {
			t.Fatalf("log line %q: missing %s=", line, key)
		}
		if value == "" {
			t.Errorf("log line %q: %s= is empty, want a value", line, key)
		}
	}
	return fields
}

// isLowerHex reports whether s is non-empty and made of lowercase hexadecimal characters only.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func TestRequestIDMiddleware(t *testing.T) {
	// wantID, when set, is the id every response must echo back; otherwise each request gets a
	// freshly generated 32 character hex id, and requests must not share one.
	tests := []struct {
		name     string
		incoming string
		requests int
		wantID   string
	}{
		{
			name:     "generates a 32 character hex id",
			requests: 1,
		},
		{
			name:     "gives each request its own id",
			requests: 2,
		},
		{
			name:     "reuses an incoming id",
			incoming: "abc-123",
			requests: 1,
			wantID:   "abc-123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured bytes.Buffer
			router := newTestRouterWithLog(t, &captured)

			ids := make(map[string]bool, tc.requests)
			for i := 0; i < tc.requests; i++ {
				rec := doRequestWithID(t, router, http.MethodGet, "/health", tc.incoming)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
				}

				id := rec.Header().Get(RequestIDHeader)
				if tc.wantID != "" {
					if id != tc.wantID {
						t.Errorf("X-Request-ID = %q, want %q", id, tc.wantID)
					}
				} else {
					if len(id) != 32 {
						t.Errorf("X-Request-ID = %q, want 32 characters", id)
					}
					if !isLowerHex(id) {
						t.Errorf("X-Request-ID = %q, want lowercase hexadecimal characters only", id)
					}
				}
				ids[id] = true
			}

			if len(ids) != tc.requests {
				t.Errorf("distinct request ids = %d, want %d", len(ids), tc.requests)
			}

			lines := capturedLines(&captured)
			if len(lines) != tc.requests {
				t.Fatalf("logged %d lines, want %d (%q)", len(lines), tc.requests, captured.String())
			}
			for _, line := range lines {
				fields := requireLogFields(t, line)
				if !ids[fields["request_id"]] {
					t.Errorf("log line %q: request_id = %q, want one of the response ids", line, fields["request_id"])
				}
			}
		})
	}
}

func TestRequestIDInContext(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
	}{
		{
			name: "generated id reaches the handler",
		},
		{
			name:     "incoming id reaches the handler",
			incoming: "abc-123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured bytes.Buffer
			router := newTestRouterWithLog(t, &captured)
			router.GET("/request-id", func(c *gin.Context) {
				c.String(http.StatusOK, "%s", c.GetString(RequestIDKey))
			})

			rec := doRequestWithID(t, router, http.MethodGet, "/request-id", tc.incoming)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
			}
			headerID := rec.Header().Get(RequestIDHeader)
			if headerID == "" {
				t.Fatal("X-Request-ID is empty, want the id the handler read from the context")
			}
			if got := rec.Body.String(); got != headerID {
				t.Errorf("handler read request id %q, response header carries %q", got, headerID)
			}
			if tc.incoming != "" && headerID != tc.incoming {
				t.Errorf("X-Request-ID = %q, want %q", headerID, tc.incoming)
			}
		})
	}
}

func TestAccessLog(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "health request is logged",
			path:       "/health",
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown route is logged",
			path:       "/nope",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured bytes.Buffer
			router := newTestRouterWithLog(t, &captured)

			rec := doRequest(t, router, http.MethodGet, tc.path, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			lines := capturedLines(&captured)
			if len(lines) != 1 {
				t.Fatalf("logged %d lines, want exactly 1 (%q)", len(lines), captured.String())
			}
			fields := requireLogFields(t, lines[0])

			if got, want := fields["method"], http.MethodGet; got != want {
				t.Errorf("method = %q, want %q", got, want)
			}
			if got, want := fields["path"], tc.path; got != want {
				t.Errorf("path = %q, want %q", got, want)
			}
			if got, want := fields["status"], strconv.Itoa(tc.wantStatus); got != want {
				t.Errorf("status = %q, want %q", got, want)
			}
			if _, err := time.ParseDuration(fields["duration"]); err != nil {
				t.Errorf("duration = %q, want a time.Duration: %v", fields["duration"], err)
			}
			if got, want := fields["request_id"], rec.Header().Get(RequestIDHeader); got != want {
				t.Errorf("request_id = %q, want the response id %q", got, want)
			}
		})
	}
}

func TestAccessLogWhenHandlerPanics(t *testing.T) {
	// gin.Recovery writes the panic stack to gin.DefaultErrorWriter, not to the injected log
	// writer; send it to io.Discard so the "exactly one line" assertion covers only the access log
	// and the test's own output stays readable.
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = io.Discard
	t.Cleanup(func() { gin.DefaultErrorWriter = previousWriter })

	var captured bytes.Buffer
	router := newTestRouterWithLog(t, &captured)
	router.GET("/boom", func(*gin.Context) {
		panic("boom")
	})

	rec := doRequest(t, router, http.MethodGet, "/boom", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if id := rec.Header().Get(RequestIDHeader); id == "" {
		t.Error("X-Request-ID is empty, want the id the request was served with")
	}

	lines := capturedLines(&captured)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly 1 (%q)", len(lines), captured.String())
	}
	fields := requireLogFields(t, lines[0])

	if got, want := fields["status"], strconv.Itoa(http.StatusInternalServerError); got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := fields["method"], http.MethodGet; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := fields["path"], "/boom"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestNewRouterWithNilLogWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.Open(database.NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}

	// A nil writer must disable the log instead of panicking or falling back to the process
	// stdout. Capture stdout so a stray log line would be caught: the requests have to succeed,
	// the router has to stay usable, and nothing may be written.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating stdout pipe: %v", err)
	}
	previousStdout := os.Stdout
	os.Stdout = writeEnd
	t.Cleanup(func() {
		os.Stdout = previousStdout
		readEnd.Close()
	})

	router := NewRouter(db, nil, testJWTSecret)

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "health still answers",
			path:       "/health",
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown route is still a 404",
			path:       "/nope",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, router, http.MethodGet, tc.path, "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if id := rec.Header().Get(RequestIDHeader); id == "" {
				t.Error("X-Request-ID is empty, want a generated id")
			}
		})
	}

	// Closing the write end lets the read below finish, then a nil writer must have produced no
	// output at all.
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("closing stdout pipe: %v", err)
	}
	captured, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if len(captured) != 0 {
		t.Errorf("stdout = %q, want no log line", captured)
	}
}
