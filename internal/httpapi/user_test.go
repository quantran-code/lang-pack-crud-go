package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/quantran-code/lang-pack-crud-go/internal/database"
	"gorm.io/gorm"
)

// jsonKeys marshals value and returns its top-level keys, so a test can assert the exact JSON shape
// a type has on the wire.
func jsonKeys(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding %T: %v", value, err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("decoding %q: %v", payload, err)
	}
	return body
}

func TestRegisterRequestValidate(t *testing.T) {
	// wantMsg is the exact message the request must be rejected with; an empty wantMsg means the
	// request is expected to pass validation. The password cases count characters for the minimum
	// and bytes for the maximum, so a multi-byte password exercises both.
	tests := []struct {
		name     string
		username string
		password string
		wantMsg  string
	}{
		{
			name:     "shortest allowed username",
			username: "abc",
			password: "password",
		},
		{
			name:     "longest allowed username",
			username: strings.Repeat("a", maxUsernameLength),
			password: "password",
		},
		{
			name:     "digits and underscore are allowed",
			username: "user_01",
			password: "password",
		},
		{
			name:     "empty username",
			username: "",
			password: "password",
			wantMsg:  usernameMessage,
		},
		{
			name:     "username one character too short",
			username: strings.Repeat("a", minUsernameLength-1),
			password: "password",
			wantMsg:  usernameMessage,
		},
		{
			name:     "username one character too long",
			username: strings.Repeat("a", maxUsernameLength+1),
			password: "password",
			wantMsg:  usernameMessage,
		},
		{
			name:     "uppercase username",
			username: "User",
			password: "password",
			wantMsg:  usernameMessage,
		},
		{
			name:     "username with a dash",
			username: "user-name",
			password: "password",
			wantMsg:  usernameMessage,
		},
		{
			name:     "username with a space",
			username: "user name",
			password: "password",
			wantMsg:  usernameMessage,
		},
		{
			name:     "username with a non-ascii character",
			username: "usér",
			password: "password",
			wantMsg:  usernameMessage,
		},
		{
			name:     "shortest allowed password",
			username: "alice",
			password: strings.Repeat("p", minPasswordCharacters),
		},
		{
			name:     "longest allowed password",
			username: "alice",
			password: strings.Repeat("p", maxPasswordBytes),
		},
		{
			name:     "empty password",
			username: "alice",
			password: "",
			wantMsg:  passwordTooShortMessage,
		},
		{
			name:     "password one character too short",
			username: "alice",
			password: strings.Repeat("p", minPasswordCharacters-1),
			wantMsg:  passwordTooShortMessage,
		},
		{
			name:     "password one byte too long",
			username: "alice",
			password: strings.Repeat("p", maxPasswordBytes+1),
			wantMsg:  passwordTooLongMessage,
		},
		{
			name:     "password minimum counts characters",
			username: "alice",
			password: strings.Repeat("é", minPasswordCharacters),
		},
		{
			name:     "password maximum counts bytes",
			username: "alice",
			password: strings.Repeat("é", maxPasswordBytes/2+1),
			wantMsg:  passwordTooLongMessage,
		},
		{
			name:     "username is checked before password",
			username: "AB",
			password: "short",
			wantMsg:  usernameMessage,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := registerRequest{Username: tc.username, Password: tc.password}.validate()

			if tc.wantMsg == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate() = nil, want %q", tc.wantMsg)
			}
			if got := err.Error(); got != tc.wantMsg {
				t.Errorf("validate() = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

func TestPasswordHashing(t *testing.T) {
	const password = "correct horse"

	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	if hash == password {
		t.Error("hash equals the plaintext password, want a bcrypt hash")
	}
	if !checkPassword(hash, password) {
		t.Error("checkPassword rejected the password the hash was made from")
	}

	for _, wrong := range []string{"", "correct hors", "Correct horse", password + " "} {
		if checkPassword(hash, wrong) {
			t.Errorf("checkPassword accepted %q, want a mismatch", wrong)
		}
	}

	// Each hash carries its own salt, so the same password hashes differently and both hashes verify.
	again, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashing password a second time: %v", err)
	}
	if again == hash {
		t.Error("two hashes of the same password are equal, want a fresh salt each time")
	}
	if !checkPassword(again, password) {
		t.Error("checkPassword rejected the second hash of the same password")
	}

	// A stored value that is not a bcrypt hash at all is a mismatch, not a crash.
	if checkPassword("not-a-bcrypt-hash", password) {
		t.Error("checkPassword accepted a malformed hash")
	}
}

func TestUserJSONOmitsPasswordHash(t *testing.T) {
	const hash = "hashed-secret-value"
	user := User{ID: 7, Username: "alice", PasswordHash: hash, Role: RoleAdmin}
	wantKeys := []string{"id", "username", "role"}

	shapes := []struct {
		name  string
		value any
	}{
		{name: "User", value: user},
		{name: "userResponse", value: newUserResponse(user)},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			body := jsonKeys(t, shape.value)
			if len(body) != len(wantKeys) {
				t.Fatalf("%s keys = %v, want exactly %v", shape.name, body, wantKeys)
			}
			for _, key := range wantKeys {
				if _, ok := body[key]; !ok {
					t.Errorf("%s has no %q key, want %v", shape.name, key, wantKeys)
				}
			}
			for _, leaked := range []string{"password_hash", "password", "PasswordHash"} {
				if _, ok := body[leaked]; ok {
					t.Errorf("%s carries a %q key, want no field for the password hash", shape.name, leaked)
				}
			}
		})
	}

	// The hash must not reach the wire as a value either, whatever key it would have been under.
	payload, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("encoding user: %v", err)
	}
	if strings.Contains(string(payload), hash) {
		t.Errorf("marshalled user = %s, want it to carry no password hash", payload)
	}
}

// newUserTestDB opens a database with the users table migrated, so a bootstrap test can call
// EnsureAdminUser without building a whole router.
func newUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Open(database.NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatalf("migrating the users table: %v", err)
	}
	return db
}

func TestEnsureAdminUserCreatesOnce(t *testing.T) {
	const (
		username = "root"
		password = "root-password"
	)
	t.Setenv("ADMIN_USERNAME", username)
	t.Setenv("ADMIN_PASSWORD", password)
	db := newUserTestDB(t)

	// Running startup twice is the case that matters: the second call must find the row the first
	// one created and leave it alone instead of adding another.
	for i := 1; i <= 2; i++ {
		if err := EnsureAdminUser(db); err != nil {
			t.Fatalf("EnsureAdminUser call %d: %v", i, err)
		}
	}

	var users []User
	if err := db.Find(&users).Error; err != nil {
		t.Fatalf("loading users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %d, want exactly one row for %q", len(users), username)
	}
	created := users[0]
	if created.ID == 0 {
		t.Error("id = 0, want a generated id")
	}
	if created.Username != username {
		t.Errorf("username = %q, want %q", created.Username, username)
	}
	if created.Role != RoleAdmin {
		t.Errorf("role = %q, want %q", created.Role, RoleAdmin)
	}
	if created.PasswordHash == password {
		t.Error("password hash equals the plaintext, want a bcrypt hash")
	}
	if !checkPassword(created.PasswordHash, password) {
		t.Error("checkPassword rejected the password the bootstrap was given")
	}
}

func TestEnsureAdminUserRejectsExistingUserRole(t *testing.T) {
	const username = "root"
	t.Setenv("ADMIN_USERNAME", username)
	t.Setenv("ADMIN_PASSWORD", "root-password")
	db := newUserTestDB(t)

	hash, err := hashPassword("existing-password")
	if err != nil {
		t.Fatalf("hashing password: %v", err)
	}
	existing := User{Username: username, PasswordHash: hash, Role: RoleUser}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seeding the existing user: %v", err)
	}

	err = EnsureAdminUser(db)
	if err == nil {
		t.Fatal("EnsureAdminUser = nil, want an error naming the conflicting role")
	}
	for _, want := range []string{username, string(RoleUser)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}

	// The conflicting row must be untouched: still one row, still the plain user role, with the
	// password it already had.
	var users []User
	if err := db.Find(&users).Error; err != nil {
		t.Fatalf("loading users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %d, want the one seeded row", len(users))
	}
	if users[0].Role != RoleUser {
		t.Errorf("role = %q, want the unchanged %q", users[0].Role, RoleUser)
	}
	if users[0].PasswordHash != hash {
		t.Error("password hash changed, want the seeded one")
	}
}

func TestEnsureAdminUserNoopWithoutEnv(t *testing.T) {
	// Either variable being empty makes the bootstrap a no-op, so no row may be created.
	tests := []struct {
		name     string
		username string
		password string
	}{
		{
			name: "neither variable set",
		},
		{
			name:     "username unset",
			password: "root-password",
		},
		{
			name:     "password unset",
			username: "root",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_USERNAME", tc.username)
			t.Setenv("ADMIN_PASSWORD", tc.password)
			db := newUserTestDB(t)

			if err := EnsureAdminUser(db); err != nil {
				t.Fatalf("EnsureAdminUser = %v, want nil", err)
			}

			var count int64
			if err := db.Model(&User{}).Count(&count).Error; err != nil {
				t.Fatalf("counting users: %v", err)
			}
			if count != 0 {
				t.Errorf("users = %d, want no row", count)
			}
		})
	}

	// A no-op must not even look: a database without the users table is left untouched.
	t.Run("does not touch a database without the users table", func(t *testing.T) {
		t.Setenv("ADMIN_USERNAME", "")
		t.Setenv("ADMIN_PASSWORD", "")
		db, err := database.Open(database.NewMemoryDSN())
		if err != nil {
			t.Fatalf("opening database: %v", err)
		}

		if err := EnsureAdminUser(db); err != nil {
			t.Fatalf("EnsureAdminUser = %v, want nil", err)
		}
	})
}
