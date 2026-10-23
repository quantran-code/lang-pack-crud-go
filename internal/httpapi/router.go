package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NewRouter builds the HTTP router. Routes are registered here, in one place.
func NewRouter(db *gorm.DB) *gin.Engine {
	if err := db.AutoMigrate(&Item{}, &Task{}); err != nil {
		panic(err)
	}

	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/health", health)
	router.POST("/tasks", createTask(db))
	router.GET("/tasks", listTasks(db))
	router.GET("/tasks/:id", getTask(db))
	router.PATCH("/tasks/:id/status", updateTaskStatus(db))
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
