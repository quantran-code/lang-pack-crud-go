package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NewRouter builds the HTTP router. Routes are registered here, in one place.
func NewRouter(db *gorm.DB) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/health", health)

	return router
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
