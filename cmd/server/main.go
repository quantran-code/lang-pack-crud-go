package main

import (
	"log"
	"os"

	"github.com/quantran-code/lang-pack-crud-go/internal/database"
	"github.com/quantran-code/lang-pack-crud-go/internal/httpapi"
)

func main() {
	db, err := database.Open(database.MemoryDSN)
	if err != nil {
		log.Fatal(err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := httpapi.NewRouter(db, os.Stdout).Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
