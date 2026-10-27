package httpapi

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// testJWTSecret is the signing secret every test router is built with, so a token built by these
// helpers is accepted by the auth middleware of a router newTestRouter returns.
const testJWTSecret = "test-jwt-secret"

// testAdminToken returns a valid token for an admin, signed with testJWTSecret. It takes no
// *testing.T so the concurrency helper can call it from a goroutine; a signing failure panics,
// because a test router without a working token would fail every request for the wrong reason.
func testAdminToken() string {
	return mustSignToken(RoleAdmin)
}

// testUserToken returns a valid token for a plain user, signed with testJWTSecret.
func testUserToken() string {
	return mustSignToken(RoleUser)
}

// mustSignToken signs a token for a fixed, non-zero user id with role, through the same issueToken
// login uses, so the helpers and the code under test agree on the claim shape.
func mustSignToken(role Role) string {
	const userID uint = 1
	token, err := issueToken(testJWTSecret, userID, role)
	if err != nil {
		panic(err)
	}
	return token
}

// signRawToken signs arbitrary claims with method and secret, so a test can build the tokens the
// middleware has to reject: one with a past exp, one signed with another secret and one signed with
// the none algorithm. The none method needs jwt's unsafe sentinel instead of a key.
func signRawToken(t *testing.T, secret string, method jwt.SigningMethod, claims jwt.MapClaims) string {
	t.Helper()

	key := any([]byte(secret))
	if method == jwt.SigningMethodNone {
		key = jwt.UnsafeAllowNoneSignatureType
	}

	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("signing token with %s: %v", method.Alg(), err)
	}
	return token
}

// authorizationHeader renders a token the way a client sends it.
func authorizationHeader(token string) string {
	return "Bearer " + token
}

// doRequestWithToken drives the real router with an Authorization header when token is non-empty and
// records the response. An empty body means no request body.
func doRequestWithToken(t *testing.T, router *gin.Engine, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", authorizationHeader(token))
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// doRequestAnon drives the real router without any Authorization header, so a test can assert what a
// request carrying no token gets.
func doRequestAnon(t *testing.T, router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithToken(t, router, "", method, path, body)
}
