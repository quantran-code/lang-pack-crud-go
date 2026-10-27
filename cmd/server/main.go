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

	// The tokens are signed and verified with this one secret, so a missing value is a
	// configuration error worth refusing to start over rather than a router without auth.
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is empty: set it to the secret that signs authentication tokens")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Building the router is what migrates the schema, so the users table exists by the time the
	// admin bootstrap looks at it.
	router := httpapi.NewRouter(db, os.Stdout, secret)
	if err := httpapi.EnsureAdminUser(db); err != nil {
		log.Fatal(err)
	}

	if err := router.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
