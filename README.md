# lang-pack-crud-go

A Go service: [Gin](https://github.com/gin-gonic/gin) for HTTP, [GORM](https://gorm.io) for data
access and SQLite (via the pure-Go `glebarez/sqlite` driver, so no cgo) for storage.

It is a scaffold for testing Octiq's Go language pack. Items, categories, tasks and stock movements
sit behind JWT authentication with an `admin` and a `user` role; features are added by tickets.

## Layout

```
cmd/server/main.go            entry point: opens the database, reads JWT_SECRET, serves the router
internal/database/            SQLite connection (database.Open)
internal/httpapi/router.go    every route is registered in NewRouter
internal/httpapi/auth.go      token signing and verification, the auth handlers, the role middleware
internal/httpapi/user.go      the User model, password hashing and the registration rules
internal/httpapi/router_test.go
```

## Run

```sh
JWT_SECRET=dev-secret go run ./cmd/server   # listens on :8080, or $PORT
curl localhost:8080/health                  # {"status":"ok"}
```

| Variable | Required | Meaning |
| --- | --- | --- |
| `JWT_SECRET` | yes | HS256 key that signs and verifies tokens. Empty or unset makes startup exit non-zero with a message. |
| `PORT` | no | Listen port; `8080` when unset. |
| `ADMIN_USERNAME` | no | Together with `ADMIN_PASSWORD`, the admin account startup creates when it does not exist yet. |
| `ADMIN_PASSWORD` | no | Password of that admin account. |

The database is in memory (`database.MemoryDSN`) and is empty on every start. Tests open their own
database with `database.NewMemoryDSN()`, so rows written by one test never show up in another.

## Endpoints

| Method and path | Access |
| --- | --- |
| `GET /health` | public |
| `POST /auth/register` | public |
| `POST /auth/login` | public |
| `GET /tasks`, `GET /tasks/:id` | any valid token |
| `POST /tasks`, `PATCH /tasks/:id/status` | `admin` |
| `GET /categories`, `GET /categories/:id` | any valid token |
| `POST /categories`, `PUT /categories/:id`, `DELETE /categories/:id` | `admin` |
| `GET /items`, `GET /items/:id`, `GET /items/:id/movements` | any valid token |
| `POST /items`, `PUT /items/:id`, `DELETE /items/:id`, `POST /items/:id/adjust` | `admin` |

Every list response carries `X-Total-Count`, the number of rows matching the filters before limit
and offset.

## Authentication

`POST /auth/register` takes `{"username": "...", "password": "..."}` and answers `201` with
`{"id": ..., "username": "...", "role": "user"}`. The username is 3 to 32 characters from `a-z`,
`0-9` and `_`, the password is at least 8 characters and at most 72 bytes (bcrypt's limit), and a
username already taken is a `409`. The password hash is never returned.

`POST /auth/login` takes the same body and answers `200` with `{"token": "<jwt>"}`. An unknown
username and a wrong password both answer `401` with the same message, so the response does not say
which one it was. Tokens are HS256, carry the user id, the role and an expiry one hour out, and are
trusted for that hour without another database lookup.

Send the token on every protected request:

```sh
curl -H "Authorization: Bearer $TOKEN" localhost:8080/items
```

A missing, malformed, wrongly signed or expired token, or one signed with any algorithm other than
HS256, is a `401` carrying `WWW-Authenticate: Bearer`. A valid token whose role is not allowed for
the route is a `403`.

A handler reads the authenticated user from the request context, never from a package-level
variable: `c.GetUint(httpapi.AuthUserIDKey)` is the user id and
`httpapi.Role(c.GetString(httpapi.AuthRoleKey))` the role.

## Check

```sh
gofmt -l .        # prints nothing
go build ./...
go vet ./...
go test ./...
```
