package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader is the header a client may send a request ID in and the header every response
// carries the ID it was served with.
const RequestIDHeader = "X-Request-ID"

// RequestIDKey is the documented gin.Context key holding the request ID string of the current
// request. Handlers read it with c.GetString(RequestIDKey); the access log reads it the same way.
const RequestIDKey = "request_id"

// maxRequestIDLength is the longest incoming X-Request-ID, in bytes, that is reused. A longer one
// is replaced with a freshly generated ID instead, so an oversized client value is never echoed
// back, stored in the context or pinned into the access log. The length is compared against
// len(id), which is the byte length.
const maxRequestIDLength int = 128

// statusInternalServerError is http.StatusInternalServerError. The numeric constant is spelled out
// here so this file's imports stay the standard-library pieces it needs plus gin.
const statusInternalServerError = 500

// newRequestID returns a new random request ID: 16 bytes read from crypto/rand, hex encoded, which
// is 32 lowercase hexadecimal characters.
func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating request id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// requestIDMiddleware makes every request carry a request ID. An incoming RequestIDHeader of up to
// maxRequestIDLength bytes is reused, anything else — absent, empty or too long — gets a freshly
// generated ID. The ID is put on the response header and in the gin context before c.Next(), so it
// is in place even when gin.Recovery() later writes the 500 for a panicking handler.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if len(id) > maxRequestIDLength {
			id = ""
		}
		if id == "" {
			generated, err := newRequestID()
			if err != nil {
				writeError(c, statusInternalServerError, "generating request id")
				c.Abort()
				return
			}
			id = generated
		}

		c.Header(RequestIDHeader, id)
		c.Set(RequestIDKey, id)
		c.Next()
	}
}

// logValue renders s for the access log. A value made only of printable ASCII other than space,
// '=' and '"' is written bare, so a normal request logs exactly as before. Anything else — a
// newline, a space, an '=' or '"', a non-ASCII byte, or the empty string — is written with
// strconv.Quote, so a value can neither split the logfmt line in two nor forge extra fields.
func logValue(s string) string {
	if s == "" {
		return strconv.Quote(s)
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b > 0x7e || b == ' ' || b == '=' || b == '"' {
			return strconv.Quote(s)
		}
	}
	return s
}

// accessLogMiddleware writes one logfmt line per request to w, after the rest of the chain has run.
// A nil writer disables logging instead of panicking. The line is built in one buffer and written
// with a single Write, so lines from concurrent requests never interleave.
func accessLogMiddleware(w io.Writer) gin.HandlerFunc {
	if w == nil {
		w = io.Discard
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		var line strings.Builder
		fmt.Fprintf(&line, "method=%s path=%s status=%d duration=%s request_id=%s\n",
			c.Request.Method,
			logValue(c.Request.URL.Path),
			c.Writer.Status(),
			time.Since(start),
			logValue(c.GetString(RequestIDKey)),
		)
		if _, err := io.WriteString(w, line.String()); err != nil {
			_ = c.Error(fmt.Errorf("writing access log line: %w", err))
		}
	}
}
