package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/quantran-code/lang-pack-crud-go/internal/database"
)

// newTestRouter builds the real router over a database of its own, so tests never share rows. Its
// access log is discarded; tests that assert on the log use newTestRouterWithLog instead.
func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	return newTestRouterWithLog(t, io.Discard)
}

// newTestRouterWithLog builds the real router over a database of its own and sends the access log
// lines to w, so a test can capture them. The router is built with the shared test signing secret,
// which is what the token helpers sign with.
func newTestRouterWithLog(t *testing.T, w io.Writer) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.Open(database.NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	return NewRouter(db, w, testJWTSecret)
}

func TestHealth(t *testing.T) {
	router := newTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	router := newTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
