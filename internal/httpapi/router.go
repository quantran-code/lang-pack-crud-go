package httpapi

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NewRouter builds the HTTP router. Routes are registered here, in one place. Every request is
// given a request ID and logged once to logWriter; a nil logWriter disables the access log.
func NewRouter(db *gorm.DB, logWriter io.Writer) *gin.Engine {
	if err := db.AutoMigrate(&Item{}, &Task{}, &Category{}); err != nil {
		panic(err)
	}

	router := gin.New()
	// Order matters: the request ID is outermost so the ID exists for the log line, the access log
	// sits outside Recovery so a panicking request still gets its line, and Recovery comes last so
	// the panic has become a 500 before the log reads the status.
	router.Use(requestIDMiddleware(), accessLogMiddleware(logWriter), gin.Recovery())

	router.GET("/health", health)
	router.POST("/tasks", createTask(db))
	router.GET("/tasks", listTasks(db))
	router.GET("/tasks/:id", getTask(db))
	router.PATCH("/tasks/:id/status", updateTaskStatus(db))
	router.POST("/categories", createCategory(db))
	router.GET("/categories", listCategories(db))
	router.GET("/categories/:id", getCategory(db))
	router.PUT("/categories/:id", updateCategory(db))
	router.DELETE("/categories/:id", deleteCategory(db))
	router.POST("/items", createItem(db))
	router.GET("/items", listItems(db))
	router.GET("/items/:id", getItem(db))
	router.PUT("/items/:id", updateItem(db))
	router.DELETE("/items/:id", deleteItem(db))

	return router
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
