package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// authTestSecret is the secret the token unit tests sign and verify with. Nothing here goes through
// HTTP, so it is deliberately not the secret a test router is built with.
const authTestSecret = "auth-test-secret"

// signClaims signs claims with method and key, so a test can build the tokens parseToken has to
// reject: an expired one, one signed with another secret, one signed with the none algorithm and one
// signed with RS256. A signing failure fails the test, because a case that cannot build its token
// proves nothing.
func signClaims(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("signing token with %s: %v", method.Alg(), err)
	}
	return signed
}

// liveClaims is a valid claim set for userID with the given role, expiring one tokenLifetime from
// now, which is what issueToken writes.
func liveClaims(userID uint, role Role) jwt.MapClaims {
	return jwt.MapClaims{
		"sub":  userID,
		"role": string(role),
		"exp":  time.Now().Add(tokenLifetime).Unix(),
	}
}

func TestIssueAndParseToken(t *testing.T) {
	tests := []struct {
		name   string
		userID uint
		role   Role
	}{
		{
			name:   "admin",
			userID: 1,
			role:   RoleAdmin,
		},
		{
			name:   "user",
			userID: 42,
			role:   RoleUser,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := issueToken(authTestSecret, tc.userID, tc.role)
			if err != nil {
				t.Fatalf("issueToken: %v", err)
			}

			userID, role, err := parseToken(authTestSecret, raw)
			if err != nil {
				t.Fatalf("parseToken: %v", err)
			}
			if userID != tc.userID {
				t.Errorf("user id = %d, want %d", userID, tc.userID)
			}
			if role != tc.role {
				t.Errorf("role = %q, want %q", role, tc.role)
			}

			// The claims are part of the contract, so read them back out of the signed token: the
			// role is the one asked for and the expiry is one hour after the issue, because that is
			// how long the middleware trusts these claims instead of re-reading the user.
			claims := jwt.MapClaims{}
			if _, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
				return []byte(authTestSecret), nil
			}); err != nil {
				t.Fatalf("parsing the issued token: %v", err)
			}
			if got := claims["role"]; got != string(tc.role) {
				t.Errorf("role claim = %v, want %q", got, string(tc.role))
			}
			exp, ok := claims["exp"].(float64)
			if !ok {
				t.Fatalf("exp claim = %v (%T), want a number", claims["exp"], claims["exp"])
			}
			wantExp := time.Now().Add(tokenLifetime).Unix()
			if got := int64(exp); got < wantExp-2 || got > wantExp+2 {
				t.Errorf("exp = %d, want about %d", got, wantExp)
			}
		})
	}
}

func TestParseTokenRejects(t *testing.T) {
	// Each case builds its raw token in the body, because the expired, foreign and algorithm cases
	// need the current time, a second secret or a key of their own. secret overrides the verification
	// secret for the case that calls parseToken with an empty one. wantMsg, when set, is a fragment
	// of the error parseToken must return, so a case fails on the wrong rejection as well as on
	// acceptance.
	tests := []struct {
		name    string
		secret  *string
		raw     func(t *testing.T) string
		wantMsg string
	}{
		{
			name:    "empty token",
			raw:     func(*testing.T) string { return "" },
			wantMsg: "malformed",
		},
		{
			name:    "not a jwt at all",
			raw:     func(*testing.T) string { return "not-a-token" },
			wantMsg: "malformed",
		},
		{
			name: "expired token",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), jwt.MapClaims{
					"sub":  1,
					"role": string(RoleAdmin),
					"exp":  time.Now().Add(-time.Minute).Unix(),
				})
			},
			wantMsg: "expired",
		},
		{
			name: "signed with another secret",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte("another-secret"), liveClaims(1, RoleAdmin))
			},
			wantMsg: "signature",
		},
		{
			name: "signed with the secret but verified with an empty one",
			secret: func() *string {
				empty := ""
				return &empty
			}(),
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), liveClaims(1, RoleAdmin))
			},
			wantMsg: "signature",
		},
		{
			name: "none algorithm",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, liveClaims(1, RoleAdmin))
			},
			wantMsg: "signing method",
		},
		{
			name: "rs256 signature",
			raw: func(t *testing.T) string {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatalf("generating RSA key: %v", err)
				}
				return signClaims(t, jwt.SigningMethodRS256, key, liveClaims(1, RoleAdmin))
			},
			wantMsg: "signing method",
		},
		{
			name: "tampered signature",
			raw: func(t *testing.T) string {
				raw := signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), liveClaims(1, RoleAdmin))
				last := raw[len(raw)-1]
				replacement := byte('A')
				if last == 'A' {
					replacement = 'B'
				}
				return raw[:len(raw)-1] + string(replacement)
			},
		},
		{
			name: "role outside admin and user",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), jwt.MapClaims{
					"sub":  1,
					"role": "superuser",
					"exp":  time.Now().Add(tokenLifetime).Unix(),
				})
			},
			wantMsg: "role",
		},
		{
			name: "role claim that is not a string",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), jwt.MapClaims{
					"sub":  1,
					"role": 7,
					"exp":  time.Now().Add(tokenLifetime).Unix(),
				})
			},
			wantMsg: "role",
		},
		{
			name: "missing sub",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), jwt.MapClaims{
					"role": string(RoleAdmin),
					"exp":  time.Now().Add(tokenLifetime).Unix(),
				})
			},
			wantMsg: "sub",
		},
		{
			name: "sub that is not a number",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), jwt.MapClaims{
					"sub":  "abc",
					"role": string(RoleAdmin),
					"exp":  time.Now().Add(tokenLifetime).Unix(),
				})
			},
			wantMsg: "sub",
		},
		{
			name: "zero sub",
			raw: func(t *testing.T) string {
				return signClaims(t, jwt.SigningMethodHS256, []byte(authTestSecret), liveClaims(0, RoleAdmin))
			},
			wantMsg: "sub",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			secret := authTestSecret
			if tc.secret != nil {
				secret = *tc.secret
			}

			userID, role, err := parseToken(secret, tc.raw(t))

			if err == nil {
				t.Fatalf("parseToken accepted the token, want an error containing %q", tc.wantMsg)
			}
			if tc.wantMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantMsg)) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantMsg)
			}
			// A rejected token must never hand back an identity to set on the context.
			if userID != 0 {
				t.Errorf("user id = %d, want 0 on a rejected token", userID)
			}
			if role != "" {
				t.Errorf("role = %q, want the empty role on a rejected token", role)
			}
		})
	}
}

func TestBearerToken(t *testing.T) {
	const token = "header.payload.signature"

	tests := []struct {
		name   string
		header string
		want   string
		ok     bool
	}{
		{
			name:   "bearer scheme",
			header: "Bearer " + token,
			want:   token,
			ok:     true,
		},
		{
			name:   "empty header",
			header: "",
		},
		{
			name:   "bearer scheme with no token",
			header: "Bearer ",
		},
		{
			name:   "bearer scheme with no space",
			header: "Bearer" + token,
		},
		{
			name:   "lowercase scheme",
			header: "bearer " + token,
		},
		{
			name:   "another scheme",
			header: "Basic " + token,
		},
		{
			name:   "token alone",
			header: token,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := bearerToken(tc.header)

			if ok != tc.ok {
				t.Fatalf("bearerToken(%q) ok = %v, want %v", tc.header, ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("bearerToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
