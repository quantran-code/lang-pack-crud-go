package httpapi

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NewRouter builds the HTTP router. Routes are registered here, in one place. Every request is
// given a request ID and logged once to logWriter; a nil logWriter disables the access log.
//
// secret is the key tokens are signed and verified with, so an empty one is a configuration error
// rather than a router that would trust tokens signed with nothing: NewRouter panics on it.
//
// Authentication is attached per route, not with router.Use, so GET /health, POST /auth/register,
// POST /auth/login and unregistered paths keep answering without a token: every other GET needs a
// token of either role, and every POST, PUT, PATCH and DELETE on items, categories, tasks and stock
// needs an admin one.
func NewRouter(db *gorm.DB, logWriter io.Writer, secret string) *gin.Engine {
	if secret == "" {
		panic("httpapi: NewRouter requires a non-empty JWT signing secret")
	}

	if err := db.AutoMigrate(&Item{}, &Task{}, &Category{}, &StockMovement{}, &User{}); err != nil {
		panic(err)
	}

	router := gin.New()
	// Order matters: the request ID is outermost so the ID exists for the log line, the access log
	// sits outside Recovery so a panicking request still gets its line, and Recovery comes last so
	// the panic has become a 500 before the log reads the status.
	router.Use(requestIDMiddleware(), accessLogMiddleware(logWriter), gin.Recovery())

	// Any signed-in user may read; only an admin may change anything. Both are the same middleware
	// with a different set of allowed roles, and neither holds shared state between requests.
	signedIn := requireRole(secret, RoleUser, RoleAdmin)
	adminOnly := requireRole(secret, RoleAdmin)

	router.GET("/health", health)
	router.POST("/auth/register", registerUser(db))
	router.POST("/auth/login", loginUser(db, secret))

	router.POST("/tasks", adminOnly, createTask(db))
	router.GET("/tasks", signedIn, listTasks(db))
	router.GET("/tasks/:id", signedIn, getTask(db))
	router.PATCH("/tasks/:id/status", adminOnly, updateTaskStatus(db))
	router.POST("/categories", adminOnly, createCategory(db))
	router.GET("/categories", signedIn, listCategories(db))
	router.GET("/categories/:id", signedIn, getCategory(db))
	router.PUT("/categories/:id", adminOnly, updateCategory(db))
	router.DELETE("/categories/:id", adminOnly, deleteCategory(db))
	router.POST("/items", adminOnly, createItem(db))
	router.GET("/items", signedIn, listItems(db))
	router.GET("/items/:id", signedIn, getItem(db))
	router.PUT("/items/:id", adminOnly, updateItem(db))
	router.DELETE("/items/:id", adminOnly, deleteItem(db))

	router.POST("/items/:id/adjust", adminOnly, adjustItem(db))
	router.GET("/items/:id/movements", signedIn, listItemMovements(db))

	return router
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
