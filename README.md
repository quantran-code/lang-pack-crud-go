# lang-pack-crud-go

A bare Go service: [Gin](https://github.com/gin-gonic/gin) for HTTP, [GORM](https://gorm.io) for data
access and SQLite (via the pure-Go `glebarez/sqlite` driver, so no cgo) for storage.

It is a scaffold for testing Octiq's Go language pack. Only `GET /health` exists; features are added
by tickets.

## Layout

```
cmd/server/main.go            entry point: opens the database, serves the router
internal/database/            SQLite connection (database.Open)
internal/httpapi/router.go    every route is registered in NewRouter
internal/httpapi/router_test.go
```

## Run

```sh
go run ./cmd/server          # listens on :8080, or $PORT
curl localhost:8080/health   # {"status":"ok"}
```

The database is in memory (`database.MemoryDSN`) and is empty on every start. Tests open their own
database with `database.NewMemoryDSN()`, so rows written by one test never show up in another.

## Check

```sh
gofmt -l .        # prints nothing
go build ./...
go vet ./...
go test ./...
```
