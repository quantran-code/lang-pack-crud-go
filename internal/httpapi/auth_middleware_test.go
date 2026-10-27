package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/quantran-code/lang-pack-crud-go/internal/database"
)

func TestAuthRejectsTokens(t *testing.T) {
	// Every case sends a protected route an Authorization header it has to refuse. An empty header
	// means the request carries none at all, and each token case builds its own token, because the
	// expired, foreign and algorithm cases need a past clock, a second secret or a key of their own.
	tests := []struct {
		name   string
		header func(t *testing.T) string
	}{
		{
			name:   "no authorization header",
			header: func(*testing.T) string { return "" },
		},
		{
			name:   "another scheme",
			header: func(*testing.T) string { return "Basic dXNlcjpwYXNzd29yZA==" },
		},
		{
			name:   "token without the bearer scheme",
			header: func(*testing.T) string { return testAdminToken() },
		},
		{
			name:   "bearer scheme with no token",
			header: func(*testing.T) string { return authorizationHeader("") },
		},
		{
			name:   "not a jwt",
			header: func(*testing.T) string { return authorizationHeader("not-a-token") },
		},
		{
			name: "signed with another secret",
			header: func(t *testing.T) string {
				return authorizationHeader(signRawToken(t, "another-secret", jwt.SigningMethodHS256, liveClaims(1, RoleAdmin)))
			},
		},
		{
			name: "expired token",
			header: func(t *testing.T) string {
				// Signed with the router's own secret, so the expiry is the only thing wrong with it.
				return authorizationHeader(signRawToken(t, testJWTSecret, jwt.SigningMethodHS256, jwt.MapClaims{
					"sub":  1,
					"role": string(RoleAdmin),
					"exp":  time.Now().Add(-time.Minute).Unix(),
				}))
			},
		},
		{
			name: "none algorithm",
			header: func(t *testing.T) string {
				return authorizationHeader(signRawToken(t, "", jwt.SigningMethodNone, liveClaims(1, RoleAdmin)))
			},
		},
		{
			name: "rs256 signature",
			header: func(t *testing.T) string {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatalf("generating RSA key: %v", err)
				}
				return authorizationHeader(signClaims(t, jwt.SigningMethodRS256, key, liveClaims(1, RoleAdmin)))
			},
		},
		{
			name: "role outside admin and user",
			header: func(t *testing.T) string {
				return authorizationHeader(signRawToken(t, testJWTSecret, jwt.SigningMethodHS256, jwt.MapClaims{
					"sub":  1,
					"role": "superuser",
					"exp":  time.Now().Add(tokenLifetime).Unix(),
				}))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)

			req := httptest.NewRequest(http.MethodGet, "/items", nil)
			if header := tc.header(t); header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
			// The 401 has to tell a client the endpoint wants a bearer token.
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want %q", got, "Bearer")
			}
			requireError(t, rec)
		})
	}
}

func TestPublicRoutesNeedNoToken(t *testing.T) {
	const (
		username = "alice"
		password = "public-password"
	)

	router := newTestRouter(t)
	// Registering through the public endpoint is what gives the login case an account to use, and
	// registering without a token is itself part of what this test covers.
	mustRegister(t, router, username, password)

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name:       "health",
			method:     http.MethodGet,
			path:       "/health",
			wantStatus: http.StatusOK,
		},
		{
			name:       "register",
			method:     http.MethodPost,
			path:       "/auth/register",
			body:       credentialsBody("bob", "bob-password"),
			wantStatus: http.StatusCreated,
		},
		{
			name:       "login",
			method:     http.MethodPost,
			path:       "/auth/login",
			body:       credentialsBody(username, password),
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown path is still a 404, not a 401",
			method:     http.MethodGet,
			path:       "/nope",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequestAnon(t, router, tc.method, tc.path, tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			// Nothing was gated, so nothing may ask for a token.
			if got := rec.Header().Get("WWW-Authenticate"); got != "" {
				t.Errorf("WWW-Authenticate = %q, want no header on a public route", got)
			}
			if tc.path == "/health" {
				if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
					t.Errorf("body = %s, want %s", got, want)
				}
			}
		})
	}
}

// matrixSeed prepares whatever row a role-matrix request targets and returns the path and body to
// send afterwards, so every gated request runs against a row that really exists.
type matrixSeed func(t *testing.T, router *gin.Engine) (path, body string)

// matrixSeedRoute is a seed for a case that targets a fixed route and needs no rows of its own.
func matrixSeedRoute(path, body string) matrixSeed {
	return func(*testing.T, *gin.Engine) (string, string) { return path, body }
}

// The seeds below create the one row a case targets and point the request at it.
func seedMatrixTask(t *testing.T, router *gin.Engine) (string, string) {
	task := mustCreateTask(t, router, `{"title":"matrix"}`)
	return taskPath(task.ID), ""
}

func seedMatrixTaskStatus(t *testing.T, router *gin.Engine) (string, string) {
	task := mustCreateTask(t, router, `{"title":"matrix"}`)
	return taskStatusPath(task.ID), `{"status":"DONE"}`
}

func seedMatrixCategory(t *testing.T, router *gin.Engine) (string, string) {
	category := mustCreateCategory(t, router, "matrix")
	return categoryPath(category.ID), ""
}

func seedMatrixCategoryRename(t *testing.T, router *gin.Engine) (string, string) {
	category := mustCreateCategory(t, router, "matrix")
	return categoryPath(category.ID), `{"name":"renamed"}`
}

func seedMatrixItem(t *testing.T, router *gin.Engine) (string, string) {
	item := mustCreateItem(t, router, "widget", "a thing", 2)
	return itemPath(item.ID), ""
}

func seedMatrixItemUpdate(t *testing.T, router *gin.Engine) (string, string) {
	item := mustCreateItem(t, router, "widget", "a thing", 2)
	return itemPath(item.ID), `{"name":"renamed","description":"updated","quantity":4}`
}

func seedMatrixItemMovements(t *testing.T, router *gin.Engine) (string, string) {
	item := mustCreateItem(t, router, "widget", "a thing", 3)
	return movementsPath(item.ID), ""
}

func seedMatrixItemAdjust(t *testing.T, router *gin.Engine) (string, string) {
	item := mustCreateItem(t, router, "widget", "a thing", 5)
	return adjustPath(item.ID), `{"delta":1}`
}

// requireRoleMatrix sends one request three times, each on a router of its own with its own seeded
// rows: as a plain user, as an admin and with no token at all. expectUser is what the user token
// gets — the admin status on a read, 403 on a write — and expectAdmin the route's normal answer.
func requireRoleMatrix(t *testing.T, name, method string, seed matrixSeed, expectAdmin, expectUser int) {
	t.Helper()

	tests := []struct {
		name       string
		token      func() string
		wantStatus int
	}{
		{
			name:       "admin token",
			token:      testAdminToken,
			wantStatus: expectAdmin,
		},
		{
			name:       "user token",
			token:      testUserToken,
			wantStatus: expectUser,
		},
		{
			name:       "no token",
			token:      nil,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(name+", "+tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			path, body := seed(t, router)

			var rec *httptest.ResponseRecorder
			if tc.token == nil {
				rec = doRequestAnon(t, router, method, path, body)
			} else {
				rec = doRequestWithToken(t, router, tc.token(), method, path, body)
			}

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			switch tc.wantStatus {
			case http.StatusUnauthorized:
				if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("WWW-Authenticate = %q, want %q", got, "Bearer")
				}
				requireError(t, rec)
			case http.StatusForbidden:
				requireError(t, rec)
				if got, want := errorMessage(t, rec), "insufficient role"; got != want {
					t.Errorf("error message = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestRoleMatrix(t *testing.T) {
	// Every resource gets its reads and its writes: a read has to work for both roles and a write has
	// to be refused for a plain user and allowed for an admin.
	t.Run("tasks", func(t *testing.T) {
		requireRoleMatrix(t, "list tasks", http.MethodGet, matrixSeedRoute("/tasks", ""), http.StatusOK, http.StatusOK)
		requireRoleMatrix(t, "get one task", http.MethodGet, seedMatrixTask, http.StatusOK, http.StatusOK)
		requireRoleMatrix(t, "create task", http.MethodPost, matrixSeedRoute("/tasks", `{"title":"matrix"}`), http.StatusCreated, http.StatusForbidden)
		requireRoleMatrix(t, "change task status", http.MethodPatch, seedMatrixTaskStatus, http.StatusOK, http.StatusForbidden)
	})

	t.Run("categories", func(t *testing.T) {
		requireRoleMatrix(t, "list categories", http.MethodGet, matrixSeedRoute("/categories", ""), http.StatusOK, http.StatusOK)
		requireRoleMatrix(t, "get one category", http.MethodGet, seedMatrixCategory, http.StatusOK, http.StatusOK)
		requireRoleMatrix(t, "create category", http.MethodPost, matrixSeedRoute("/categories", `{"name":"matrix"}`), http.StatusCreated, http.StatusForbidden)
		requireRoleMatrix(t, "rename category", http.MethodPut, seedMatrixCategoryRename, http.StatusOK, http.StatusForbidden)
		requireRoleMatrix(t, "delete category", http.MethodDelete, seedMatrixCategory, http.StatusNoContent, http.StatusForbidden)
	})

	t.Run("items", func(t *testing.T) {
		requireRoleMatrix(t, "list items", http.MethodGet, matrixSeedRoute("/items", ""), http.StatusOK, http.StatusOK)
		requireRoleMatrix(t, "get one item", http.MethodGet, seedMatrixItem, http.StatusOK, http.StatusOK)
		requireRoleMatrix(t, "create item", http.MethodPost, matrixSeedRoute("/items", `{"name":"matrix","description":"a thing","quantity":1}`), http.StatusCreated, http.StatusForbidden)
		requireRoleMatrix(t, "update item", http.MethodPut, seedMatrixItemUpdate, http.StatusOK, http.StatusForbidden)
		requireRoleMatrix(t, "delete item", http.MethodDelete, seedMatrixItem, http.StatusNoContent, http.StatusForbidden)
	})

	t.Run("stock", func(t *testing.T) {
		requireRoleMatrix(t, "list item movements", http.MethodGet, seedMatrixItemMovements, http.StatusOK, http.StatusOK)
		requireRoleMatrix(t, "adjust item", http.MethodPost, seedMatrixItemAdjust, http.StatusOK, http.StatusForbidden)
	})

	t.Run("a forbidden write changes no row", func(t *testing.T) {
		router := newTestRouter(t)
		category := mustCreateCategory(t, router, "alpha")
		task := mustCreateTask(t, router, `{"title":"existing"}`)
		item := mustCreateItemInCategory(t, router, "widget", "a thing", 5, &category.ID)

		// Every one of these would change a row if it got past the middleware, and each targets a row
		// that exists, so a change would show up in the checks below.
		rejected := []struct {
			name   string
			method string
			path   string
			body   string
		}{
			{"create task", http.MethodPost, "/tasks", `{"title":"sneaky"}`},
			{"change task status", http.MethodPatch, taskStatusPath(task.ID), `{"status":"DONE"}`},
			{"create category", http.MethodPost, "/categories", `{"name":"sneaky"}`},
			{"rename category", http.MethodPut, categoryPath(category.ID), `{"name":"sneaky"}`},
			{"delete category", http.MethodDelete, categoryPath(category.ID), ""},
			{"create item", http.MethodPost, "/items", `{"name":"sneaky","description":"x","quantity":1}`},
			{"update item", http.MethodPut, itemPath(item.ID), `{"name":"sneaky","description":"x","quantity":9}`},
			{"delete item", http.MethodDelete, itemPath(item.ID), ""},
			{"adjust item", http.MethodPost, adjustPath(item.ID), `{"delta":-5}`},
		}

		for _, request := range rejected {
			rec := doRequestWithToken(t, router, testUserToken(), request.method, request.path, request.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want %d (body = %s)", request.name, rec.Code, http.StatusForbidden, rec.Body.String())
			}
		}

		// The task is still the only one and still open.
		tasks := doRequest(t, router, http.MethodGet, "/tasks", "")
		if tasks.Code != http.StatusOK {
			t.Fatalf("listing tasks: status = %d, body = %s", tasks.Code, tasks.Body.String())
		}
		gotTasks := decodeTasks(t, tasks)
		if len(gotTasks) != 1 {
			t.Fatalf("tasks = %d, want only the seeded row", len(gotTasks))
		}
		requireTask(t, gotTasks[0], Task{ID: task.ID, Title: "existing", Status: StatusOpen})

		// The category is still there, under its own name.
		if names := listCategoryNames(t, router); len(names) != 1 || names[category.ID] != "alpha" {
			t.Errorf("categories = %v, want only %d named %q", names, category.ID, "alpha")
		}

		// The item still holds its quantity and its category, and nothing was recorded against it.
		rec := doRequest(t, router, http.MethodGet, itemPath(item.ID), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("reading item back: status = %d, body = %s", rec.Code, rec.Body.String())
		}
		stored := decodeItem(t, rec)
		requireItem(t, stored, Item{ID: item.ID, Name: "widget", Description: "a thing", Quantity: 5})
		requireCategoryID(t, stored.CategoryID, &category.ID)

		requireNoMovements(t, doRequest(t, router, http.MethodGet, movementsPath(item.ID), ""))
	})
}

func TestAuthUserInContext(t *testing.T) {
	router := newTestRouter(t)
	// The route is gated by the same middleware the real routes use, so the handler runs exactly
	// where a normal handler does and reads the identity the middleware put on the context.
	router.GET("/whoami", requireRole(testJWTSecret, RoleUser, RoleAdmin), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id": c.GetUint(AuthUserIDKey),
			"role":    c.GetString(AuthRoleKey),
		})
	})

	// Two different identities, so a request that saw the other one would show the user is held
	// somewhere shared instead of per request.
	tests := []struct {
		name   string
		userID uint
		role   Role
	}{
		{
			name:   "admin",
			userID: 7,
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
			token, err := issueToken(testJWTSecret, tc.userID, tc.role)
			if err != nil {
				t.Fatalf("issueToken: %v", err)
			}

			rec := doRequestWithToken(t, router, token, http.MethodGet, "/whoami", "")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
			}

			var body struct {
				UserID uint   `json:"user_id"`
				Role   string `json:"role"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding whoami body %q: %v", rec.Body.String(), err)
			}
			if body.UserID != tc.userID {
				t.Errorf("%s = %d, want %d", AuthUserIDKey, body.UserID, tc.userID)
			}
			if body.Role != string(tc.role) {
				t.Errorf("%s = %q, want %q", AuthRoleKey, body.Role, string(tc.role))
			}
		})
	}
}

func TestAuthRequestLogFields(t *testing.T) {
	token := testAdminToken()

	var captured bytes.Buffer
	router := newTestRouterWithLog(t, &captured)

	rec := doRequestWithToken(t, router, token, http.MethodGet, "/items", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	lines := capturedLines(&captured)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly 1 (%q)", len(lines), captured.String())
	}
	// requireLogFields fails unless the line carries exactly the five documented fields, so an
	// authenticated request logs nothing extra.
	fields := requireLogFields(t, lines[0])

	if got, want := fields["method"], http.MethodGet; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := fields["path"], "/items"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := fields["status"], strconv.Itoa(http.StatusOK); got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got, want := fields["request_id"], rec.Header().Get(RequestIDHeader); got != want {
		t.Errorf("request_id = %q, want the response id %q", got, want)
	}
	// The token is a credential, so it must never reach the log line.
	if strings.Contains(lines[0], token) {
		t.Errorf("log line %q carries the bearer token, want it left out", lines[0])
	}
}

func TestNewRouterPanicsOnEmptySecret(t *testing.T) {
	db, err := database.Open(database.NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}

	// Recover in a nested function so the assertions run on the test goroutine with the panic
	// already stopped.
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		NewRouter(db, io.Discard, "")
		return nil
	}()

	if recovered == nil {
		t.Fatal("NewRouter with an empty secret did not panic")
	}
	message := fmt.Sprint(recovered)
	if !strings.Contains(strings.ToLower(message), "secret") {
		t.Errorf("panic = %q, want a message naming the empty secret", message)
	}
}
