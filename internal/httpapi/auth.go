package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

// AuthUserIDKey is the documented gin.Context key holding the id of the authenticated user, as a
// uint. The auth middleware sets it once a token has been verified and handler code reads it with
// c.GetUint(AuthUserIDKey). Nothing in this package keeps the authenticated user in a package-level
// variable, so two concurrent requests can never see each other's identity.
const AuthUserIDKey = "auth_user_id"

// AuthRoleKey is the documented gin.Context key holding the role of the authenticated user, as a
// Role. The auth middleware sets it next to AuthUserIDKey and handler code reads it with
// Role(c.GetString(AuthRoleKey)).
const AuthRoleKey = "auth_role"

// signingMethodHS256 is the one algorithm a token may be signed with. It is spelled out once because
// jwt.WithValidMethods compares the algorithm by name.
const signingMethodHS256 = "HS256"

// tokenLifetime is how long an issued token stays valid: one hour. Nothing extends or revokes it and
// no request re-reads the user from the database, so the claims are trusted for exactly this long.
const tokenLifetime = time.Hour

// issueToken signs a token for one user with HS256. The claims are the user id under sub, the role,
// and exp one tokenLifetime from now; nothing else is needed, because the middleware trusts these
// claims for the token's whole life instead of looking the user up again.
func issueToken(secret string, userID uint, role Role) (string, error) {
	claims := jwt.MapClaims{
		"sub":  userID,
		"role": string(role),
		"exp":  time.Now().Add(tokenLifetime).Unix(),
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("signing token for user %d: %w", userID, err)
	}
	return signed, nil
}

// parseToken verifies raw with secret and returns the claims the handlers and the middleware need.
// Only HS256 is accepted, so a token signed with RS256, with the none algorithm or with any other
// method is rejected before its claims are looked at; jwt/v5 checks exp itself, so an expired token
// is an error too. A bad signature, a missing or non-numeric sub and a role outside admin and user
// are all errors.
func parseToken(secret, raw string) (uint, Role, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{signingMethodHS256}))
	if err != nil {
		return 0, "", fmt.Errorf("verifying token: %w", err)
	}
	if !token.Valid {
		return 0, "", errors.New("token is not valid")
	}

	userID, err := claimUserID(claims["sub"])
	if err != nil {
		return 0, "", err
	}

	rawRole, ok := claims["role"].(string)
	if !ok {
		return 0, "", errors.New("token has no string role claim")
	}
	role := Role(rawRole)
	if !role.Valid() {
		return 0, "", fmt.Errorf("token role %q is not one of %s or %s", rawRole, RoleAdmin, RoleUser)
	}

	return userID, role, nil
}

// claimUserID turns the sub claim into the user id it carries. issueToken writes it as a JSON
// number, which the parser hands back as a float64; a string or a json.Number is accepted as well,
// because the JWT specification says sub is a string and a client may send either. Zero is rejected:
// no stored user has that id.
func claimUserID(raw any) (uint, error) {
	switch value := raw.(type) {
	case float64:
		if value != math.Trunc(value) || value <= 0 {
			return 0, fmt.Errorf("token sub claim %v is not a user id", value)
		}
		return textualUserID(strconv.FormatFloat(value, 'f', -1, 64))
	case json.Number:
		return textualUserID(string(value))
	case string:
		return textualUserID(value)
	default:
		return 0, fmt.Errorf("token sub claim is %T, want a number", raw)
	}
}

// textualUserID reads a strictly decimal user id from one of the textual forms a sub claim can take.
func textualUserID(text string) (uint, error) {
	id, err := strconv.ParseUint(text, 10, 0)
	if err != nil {
		return 0, fmt.Errorf("parsing token sub claim %q: %w", text, err)
	}
	if id == 0 {
		return 0, fmt.Errorf("token sub claim %q is not a user id", text)
	}
	return uint(id), nil
}

// bearerToken extracts the token from an Authorization header value. Exactly the "Bearer <token>"
// form is accepted: the scheme is matched case-sensitively as the specification spells it, and an
// absent header, another scheme or an empty token reports ok == false.
func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// duplicateUsernameError builds the message registration answers with when the username is already
// stored. It is built in one place because the pre-check and the unique index both produce it.
func duplicateUsernameError() error {
	return errors.New("username already exists")
}

// invalidCredentialsError builds the one message login answers with, for an unknown username and
// for a wrong password, so the response never reveals which of the two it was.
func invalidCredentialsError() error {
	return errors.New("invalid username or password")
}

// usernameTaken reports whether a user already stores username exactly.
func usernameTaken(db *gorm.DB, username string) (bool, error) {
	var count int64
	if err := db.Model(&User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return false, fmt.Errorf("counting users named %q: %w", username, err)
	}
	return count > 0, nil
}

// writeUnauthorized writes the 401 every rejected request gets: the shared error envelope plus the
// WWW-Authenticate header saying the endpoint wants a bearer token, then aborts the chain.
func writeUnauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", "Bearer")
	writeError(c, http.StatusUnauthorized, "invalid or missing token")
	c.Abort()
}

// registerUser handles POST /auth/register, which is public. The body is validated before the
// database is touched, so an invalid username or password is a 400 that never reaches SQL. A
// username another user already stores is a 409. The row always carries RoleUser, because nothing
// in the body can ask for admin, and the password is stored as its bcrypt hash only. The reply is
// the userResponse DTO, so the hash has no JSON shape at all.
func registerUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req registerRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := req.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		ctx := c.Request.Context()
		taken, err := usernameTaken(db.WithContext(ctx), req.Username)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "creating user")
			return
		}
		if taken {
			writeError(c, http.StatusConflict, duplicateUsernameError().Error())
			return
		}

		hash, err := hashPassword(req.Password)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "creating user")
			return
		}

		user := User{Username: req.Username, PasswordHash: hash, Role: RoleUser}
		if err := db.WithContext(ctx).Create(&user).Error; err != nil {
			// The pre-check and the insert are two statements, so a concurrent register can slip
			// between them and trip the unique index; that is the same conflict, not a 500.
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				writeError(c, http.StatusConflict, duplicateUsernameError().Error())
				return
			}
			if nowTaken, countErr := usernameTaken(db.WithContext(ctx), req.Username); countErr == nil && nowTaken {
				writeError(c, http.StatusConflict, duplicateUsernameError().Error())
				return
			}
			writeError(c, http.StatusInternalServerError, "creating user")
			return
		}

		c.JSON(http.StatusCreated, newUserResponse(user))
	}
}

// loginUser handles POST /auth/login, which is public. The login body has the same shape as the
// registration one, so it binds through registerRequest; the registration rules are not applied
// here, because they decide what may be created, not what may sign in. The user is looked up by
// username only and the password is compared against the stored bcrypt hash; an unknown username
// and a mismatch answer the same 401 with the same message, so the response cannot be used to tell
// them apart. Success answers 200 with a token signed with secret, carrying the user id and role.
func loginUser(db *gorm.DB, secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req registerRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Username == "" || req.Password == "" {
			writeError(c, http.StatusBadRequest, "username and password are required")
			return
		}

		var user User
		err := db.WithContext(c.Request.Context()).Where("username = ?", req.Username).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusUnauthorized, invalidCredentialsError().Error())
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "logging in")
			return
		}
		if !checkPassword(user.PasswordHash, req.Password) {
			writeError(c, http.StatusUnauthorized, invalidCredentialsError().Error())
			return
		}

		token, err := issueToken(secret, user.ID, user.Role)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "logging in")
			return
		}

		c.JSON(http.StatusOK, gin.H{"token": token})
	}
}

// requireRole is the middleware every non-public route is registered with, so authentication is a
// property of each route instead of a router-wide Use. It reads the token from
// Authorization: Bearer <token> and verifies it with secret: a missing header, a malformed token, a
// bad signature, an expired exp or an algorithm other than HS256 is a 401 carrying
// WWW-Authenticate: Bearer, and a valid token whose role is not one of allowed is a 403. On success
// the user id is put on the gin context under AuthUserIDKey as a uint and the role under AuthRoleKey
// as its string value, so a handler reads them with c.GetUint and c.GetString and no package-level
// variable holds the user. The claims are trusted for the token's whole life, so nothing here
// touches the database.
func requireRole(secret string, allowed ...Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			writeUnauthorized(c)
			return
		}

		userID, role, err := parseToken(secret, raw)
		if err != nil {
			writeUnauthorized(c)
			return
		}

		for _, candidate := range allowed {
			if role == candidate {
				c.Set(AuthUserIDKey, userID)
				c.Set(AuthRoleKey, string(role))
				c.Next()
				return
			}
		}

		writeError(c, http.StatusForbidden, "insufficient role")
		c.Abort()
	}
}
