package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
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

// requestIDMiddleware makes every request carry a request ID. An incoming non-empty RequestIDHeader
// is reused, anything else gets a freshly generated ID. The ID is put on the response header and in
// the gin context before c.Next(), so it is in place even when gin.Recovery() later writes the 500
// for a panicking handler.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
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
			c.Request.URL.Path,
			c.Writer.Status(),
			time.Since(start),
			c.GetString(RequestIDKey),
		)
		if _, err := io.WriteString(w, line.String()); err != nil {
			_ = c.Error(fmt.Errorf("writing access log line: %w", err))
		}
	}
}
