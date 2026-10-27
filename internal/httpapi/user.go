package httpapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Role is the closed vocabulary of user roles. It is a named string type so the database column
// stays a plain string while Go code can only use the declared values.
type Role string

// The allowed user roles. These two constants are the only place the wire values are written down;
// every handler, middleware and lookup uses them by name.
const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Valid reports whether r is one of the declared roles. The zero value and any other string are
// invalid.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleUser:
		return true
	default:
		return false
	}
}

// User is the account the auth endpoints create and the table GORM migrates. Username carries a
// unique index; PasswordHash is bcrypt output and is tagged json:"-" so no response can leak it,
// and the auth handlers reply with userResponse instead of this struct anyway.
type User struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	Username     string `gorm:"uniqueIndex" json:"username"`
	PasswordHash string `json:"-"`
	Role         Role   `json:"role"`
}

// The username and password bounds. The username length is counted in bytes, which is the same as
// its character count because only ASCII characters are allowed. The password minimum is counted in
// runes, so a non-ASCII password counts its characters; the maximum is counted in bytes because
// that is the limit bcrypt itself enforces.
const (
	minUsernameLength     int = 3
	maxUsernameLength     int = 32
	minPasswordCharacters int = 8
	maxPasswordBytes      int = 72
)

// The one message every invalid username produces, and the two password ones. The handlers turn
// them into a 400 body.
const (
	usernameMessage         = "username must be 3 to 32 characters from a-z, 0-9 and _"
	passwordTooShortMessage = "password must be at least 8 characters"
	passwordTooLongMessage  = "password must be at most 72 bytes"
)

// registerRequest is the body accepted by POST /auth/register. Both fields are required; the stored
// password is never the one that arrived here, only its bcrypt hash.
type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// validate applies the registration rules: a username of the allowed shape and a password of at
// least minPasswordCharacters characters and at most maxPasswordBytes bytes, the latter being
// bcrypt's own limit.
func (r registerRequest) validate() error {
	if !validUsername(r.Username) {
		return errors.New(usernameMessage)
	}
	if utf8.RuneCountInString(r.Password) < minPasswordCharacters {
		return errors.New(passwordTooShortMessage)
	}
	if len(r.Password) > maxPasswordBytes {
		return errors.New(passwordTooLongMessage)
	}
	return nil
}

// userResponse is the body every endpoint that returns a user replies with. It has no field for the
// password hash at all, so the hash has no JSON shape to leak through.
type userResponse struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
}

// newUserResponse maps a stored user to the body a handler returns.
func newUserResponse(user User) userResponse {
	return userResponse{
		ID:       user.ID,
		Username: user.Username,
		Role:     user.Role,
	}
}

// validUsername reports whether username is minUsernameLength to maxUsernameLength bytes long and
// made only of a-z, 0-9 and _. Matching is exact: an uppercase letter, a dash or a space is invalid.
func validUsername(username string) bool {
	if len(username) < minUsernameLength || len(username) > maxUsernameLength {
		return false
	}
	for i := 0; i < len(username); i++ {
		b := username[i]
		switch {
		case b >= 'a' && b <= 'z':
		case b >= '0' && b <= '9':
		case b == '_':
		default:
			return false
		}
	}
	return true
}

// hashPassword returns the bcrypt hash of password at bcrypt.DefaultCost. The plaintext is never
// stored anywhere.
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hash), nil
}

// checkPassword reports whether password matches hash. Any error from bcrypt, including a malformed
// hash, is a mismatch rather than a failure the caller has to handle.
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// EnsureAdminUser is the startup bootstrap of the operator's admin account. It reads ADMIN_USERNAME
// and ADMIN_PASSWORD from the environment: when either is empty it does nothing at all, not even a
// lookup, so an unconfigured start never depends on the users table existing.
//
// With both set, an absent user is created with RoleAdmin and a bcrypt hash of ADMIN_PASSWORD — the
// registration limits do not apply here, because these values come from the operator running the
// process. A user that is already stored with RoleAdmin is left alone, so running startup twice
// leaves exactly one row. A user that is stored with any other role is a configuration conflict: it
// returns an error naming the username and the role it found, and startup must refuse to continue.
// Errors name the username and never the password.
func EnsureAdminUser(db *gorm.DB) error {
	username := os.Getenv("ADMIN_USERNAME")
	password := os.Getenv("ADMIN_PASSWORD")
	if username == "" || password == "" {
		return nil
	}

	// Startup has no request, so there is no caller context to hand down.
	ctx := context.Background()

	var user User
	err := db.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		hash, hashErr := hashPassword(password)
		if hashErr != nil {
			return fmt.Errorf("creating admin user %q: %w", username, hashErr)
		}

		admin := User{Username: username, PasswordHash: hash, Role: RoleAdmin}
		if createErr := db.WithContext(ctx).Create(&admin).Error; createErr != nil {
			return fmt.Errorf("creating admin user %q: %w", username, createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading admin user %q: %w", username, err)
	}

	if user.Role != RoleAdmin {
		return fmt.Errorf("admin user %q already exists with role %q, want %q", username, user.Role, RoleAdmin)
	}
	return nil
}
