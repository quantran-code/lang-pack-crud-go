package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/quantran-code/lang-pack-crud-go/internal/database"
	"gorm.io/gorm"
)

// credentialsBody builds the {"username": ..., "password": ...} body the register and login
// endpoints both accept.
func credentialsBody(username, password string) string {
	return fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
}

// mustRegister creates a user through the public registration endpoint, so a test can start from a
// known account. Anything but 201 fails the test, because a case whose account could not be
// registered would fail later for the wrong reason.
func mustRegister(t *testing.T, router *gin.Engine, username, password string) userResponse {
	t.Helper()
	rec := doRequestAnon(t, router, http.MethodPost, "/auth/register", credentialsBody(username, password))
	if rec.Code != http.StatusCreated {
		t.Fatalf("registering %q: status = %d, body = %s", username, rec.Code, rec.Body.String())
	}
	var created userResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding registered user from %q: %v", rec.Body.String(), err)
	}
	return created
}

// newTestRouterWithUsers builds the real router over a database the test also holds, so a register
// case can count the rows a request left behind.
func newTestRouterWithUsers(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db, err := database.Open(database.NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	return newTestRouterWithDB(t, db), db
}

// countUsers returns how many rows the users table holds.
func countUsers(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var count int64
	if err := db.Model(&User{}).Count(&count).Error; err != nil {
		t.Fatalf("counting users: %v", err)
	}
	return int(count)
}

func TestRegister(t *testing.T) {
	const password = "correct-password"

	// seed lists the usernames that already exist before the request; body overrides the generated
	// body when the case is about the envelope rather than the rules. wantMsg is the exact message a
	// rejected request must carry, and wantUsers is the users table's row count afterwards, so a
	// rejected register is shown to have added nothing.
	tests := []struct {
		name       string
		username   string
		password   string
		body       string
		seed       []string
		wantStatus int
		wantMsg    string
		wantUsers  int
	}{
		{
			name:       "valid registration",
			username:   "alice",
			password:   password,
			wantStatus: http.StatusCreated,
			wantUsers:  1,
		},
		{
			name:       "shortest allowed username",
			username:   strings.Repeat("a", minUsernameLength),
			password:   password,
			wantStatus: http.StatusCreated,
			wantUsers:  1,
		},
		{
			name:       "longest allowed username",
			username:   strings.Repeat("a", maxUsernameLength),
			password:   password,
			wantStatus: http.StatusCreated,
			wantUsers:  1,
		},
		{
			name:       "username of two characters",
			username:   strings.Repeat("a", minUsernameLength-1),
			password:   password,
			wantStatus: http.StatusBadRequest,
			wantMsg:    usernameMessage,
		},
		{
			name:       "username of thirty-three characters",
			username:   strings.Repeat("a", maxUsernameLength+1),
			password:   password,
			wantStatus: http.StatusBadRequest,
			wantMsg:    usernameMessage,
		},
		{
			name:       "uppercase username",
			username:   "Alice",
			password:   password,
			wantStatus: http.StatusBadRequest,
			wantMsg:    usernameMessage,
		},
		{
			name:       "punctuated username",
			username:   "alice!",
			password:   password,
			wantStatus: http.StatusBadRequest,
			wantMsg:    usernameMessage,
		},
		{
			name:       "password of seven characters",
			username:   "alice",
			password:   strings.Repeat("p", minPasswordCharacters-1),
			wantStatus: http.StatusBadRequest,
			wantMsg:    passwordTooShortMessage,
		},
		{
			name:       "password of seventy-three bytes",
			username:   "alice",
			password:   strings.Repeat("p", maxPasswordBytes+1),
			wantStatus: http.StatusBadRequest,
			wantMsg:    passwordTooLongMessage,
		},
		{
			name:       "duplicate username",
			username:   "alice",
			password:   password,
			seed:       []string{"alice"},
			wantStatus: http.StatusConflict,
			wantMsg:    duplicateUsernameError().Error(),
			wantUsers:  1,
		},
		{
			name:       "empty body",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    usernameMessage,
		},
		{
			name:       "malformed json",
			body:       `{"username":`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "invalid JSON body",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router, db := newTestRouterWithUsers(t)
			for _, username := range tc.seed {
				mustRegister(t, router, username, "seeded-password")
			}

			body := tc.body
			if body == "" {
				body = credentialsBody(tc.username, tc.password)
			}

			rec := doRequestAnon(t, router, http.MethodPost, "/auth/register", body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantStatus != http.StatusCreated {
				requireError(t, rec)
				if got := errorMessage(t, rec); got != tc.wantMsg {
					t.Errorf("error message = %q, want %q", got, tc.wantMsg)
				}
			} else {
				// The wire shape is part of the contract: exactly these three keys, so the password
				// hash has nowhere to leak through.
				requireFieldNames(t, rec, "id", "username", "role")

				var created userResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
					t.Fatalf("decoding registered user from %q: %v", rec.Body.String(), err)
				}
				if created.ID == 0 {
					t.Error("id = 0, want a generated id")
				}
				if created.Username != tc.username {
					t.Errorf("username = %q, want %q", created.Username, tc.username)
				}
				if created.Role != RoleUser {
					t.Errorf("role = %q, want %q", created.Role, RoleUser)
				}

				// The stored row must carry the same role and a bcrypt hash, never the plaintext.
				var stored User
				if err := db.Where("username = ?", tc.username).First(&stored).Error; err != nil {
					t.Fatalf("loading the stored user: %v", err)
				}
				if stored.Role != RoleUser {
					t.Errorf("stored role = %q, want %q", stored.Role, RoleUser)
				}
				if stored.PasswordHash == tc.password {
					t.Error("stored password equals the plaintext, want a bcrypt hash")
				}
				if !checkPassword(stored.PasswordHash, tc.password) {
					t.Error("checkPassword rejected the password the user was registered with")
				}
			}

			if got := countUsers(t, db); got != tc.wantUsers {
				t.Errorf("users = %d, want %d", got, tc.wantUsers)
			}
		})
	}
}

func TestLogin(t *testing.T) {
	const (
		username = "alice"
		password = "correct-password"
	)

	// Every case runs against one registered account, so the unknown-username and wrong-password
	// cases really are about the credentials and not about an empty table. wantMsg is the exact
	// message a rejected login must carry.
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "registered user logs in",
			body:       credentialsBody(username, password),
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown username",
			body:       credentialsBody("bob", password),
			wantStatus: http.StatusUnauthorized,
			wantMsg:    invalidCredentialsError().Error(),
		},
		{
			name:       "wrong password",
			body:       credentialsBody(username, "wrong-password"),
			wantStatus: http.StatusUnauthorized,
			wantMsg:    invalidCredentialsError().Error(),
		},
		{
			name:       "empty body",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "username and password are required",
		},
		{
			name:       "missing password",
			body:       fmt.Sprintf(`{"username":%q}`, username),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "username and password are required",
		},
		{
			name:       "malformed json",
			body:       `{"username":`,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "invalid JSON body",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			registered := mustRegister(t, router, username, password)

			rec := doRequestAnon(t, router, http.MethodPost, "/auth/login", tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				if got := errorMessage(t, rec); got != tc.wantMsg {
					t.Errorf("error message = %q, want %q", got, tc.wantMsg)
				}
				return
			}

			requireFieldNames(t, rec, "token")

			var body struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding login response %q: %v", rec.Body.String(), err)
			}
			if body.Token == "" {
				t.Fatalf("body = %s, want a token", rec.Body.String())
			}

			// The token has to be one the middleware accepts, carrying the account that logged in.
			userID, role, err := parseToken(testJWTSecret, body.Token)
			if err != nil {
				t.Fatalf("parseToken on the issued token: %v", err)
			}
			if userID != registered.ID {
				t.Errorf("token user id = %d, want %d", userID, registered.ID)
			}
			if role != RoleUser {
				t.Errorf("token role = %q, want %q", role, RoleUser)
			}
		})
	}

	t.Run("unknown username and wrong password are indistinguishable", func(t *testing.T) {
		router := newTestRouter(t)
		mustRegister(t, router, username, password)

		unknown := doRequestAnon(t, router, http.MethodPost, "/auth/login", credentialsBody("bob", password))
		wrong := doRequestAnon(t, router, http.MethodPost, "/auth/login", credentialsBody(username, "wrong-password"))

		for name, rec := range map[string]*httptest.ResponseRecorder{"unknown username": unknown, "wrong password": wrong} {
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s: status = %d, want %d (body = %s)", name, rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
			requireError(t, rec)
		}

		// The two bodies must be byte-identical, so the response cannot be used to tell which of the
		// two was wrong.
		if got, want := unknown.Body.String(), wrong.Body.String(); got != want {
			t.Errorf("unknown username body = %s, wrong password body = %s; want the same body", got, want)
		}
		if got, want := errorMessage(t, unknown), invalidCredentialsError().Error(); got != want {
			t.Errorf("error message = %q, want %q", got, want)
		}
	})
}
