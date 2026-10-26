package httpapi

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestIncomingRequestIDLengthLimit covers both sides of the byte limit on a reused incoming id: an
// id of exactly maxRequestIDLength bytes is kept, one byte more is discarded and replaced.
func TestIncomingRequestIDLengthLimit(t *testing.T) {
	tests := []struct {
		name    string
		length  int
		wantOld bool
	}{
		{
			name:    "id at the limit is reused",
			length:  maxRequestIDLength,
			wantOld: true,
		},
		{
			name:    "id over the limit is replaced",
			length:  maxRequestIDLength + 1,
			wantOld: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			incoming := strings.Repeat("a", tc.length)

			var captured bytes.Buffer
			router := newTestRouterWithLog(t, &captured)
			// /health does not expose the context id, so add a route that echoes it: the id the
			// handler reads has to be the one the response header and the log line carry.
			router.GET("/request-id", func(c *gin.Context) {
				c.String(http.StatusOK, "%s", c.GetString(RequestIDKey))
			})

			rec := doRequestWithID(t, router, http.MethodGet, "/request-id", incoming)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
			}

			id := rec.Header().Get(RequestIDHeader)
			if tc.wantOld {
				if id != incoming {
					t.Errorf("X-Request-ID = %q, want the incoming %d byte id", id, tc.length)
				}
			} else {
				if id == incoming {
					t.Errorf("X-Request-ID = %q, want a generated id instead of the incoming %d byte id", id, tc.length)
				}
				if len(id) != 32 {
					t.Errorf("X-Request-ID = %q, want 32 characters", id)
				}
				if !isLowerHex(id) {
					t.Errorf("X-Request-ID = %q, want lowercase hexadecimal characters only", id)
				}
			}

			if got := rec.Body.String(); got != id {
				t.Errorf("handler read request id %q, response header carries %q", got, id)
			}

			lines := capturedLines(&captured)
			if len(lines) != 1 {
				t.Fatalf("logged %d lines, want exactly 1 (%q)", len(lines), captured.String())
			}
			fields := logFields(t, lines[0])
			if got := fields["request_id"]; got != id {
				t.Errorf("logged request_id = %q, want the response id %q", got, id)
			}
		})
	}
}
