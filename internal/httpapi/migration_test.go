package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/quantran-code/lang-pack-crud-go/internal/database"
	"gorm.io/gorm"
)

// legacyItem is the items table as it existed before the category change: the same columns, with no
// category_id. It is only here so a test can build that older table and populate it.
type legacyItem struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	Description string
	Quantity    int
}

// TableName keeps the legacy struct on the real items table, which is the one the router migrates.
func (legacyItem) TableName() string {
	return "items"
}

// newTestRouterWithDB builds the real router over a database a test prepared itself, so a test can
// control the schema and the rows that exist before AutoMigrate runs. The router is built with the
// shared test signing secret, so the token helpers' requests pass the auth middleware.
func newTestRouterWithDB(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewRouter(db, io.Discard, testJWTSecret)
}

// TestMigrationAddsCategoryIDToItems covers the populated-table migration: an items table created
// without a category_id column, already holding a row, must gain the column when the router is
// built, and the row must survive with category_id null.
func TestMigrationAddsCategoryIDToItems(t *testing.T) {
	db, err := database.Open(database.NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}

	if err := db.AutoMigrate(&legacyItem{}); err != nil {
		t.Fatalf("creating the legacy items table: %v", err)
	}
	if err := db.Create(&legacyItem{Name: "legacy", Description: "from before", Quantity: 5}).Error; err != nil {
		t.Fatalf("inserting the legacy row: %v", err)
	}

	// Building the router is what runs AutoMigrate over the existing table.
	router := newTestRouterWithDB(t, db)

	rec := doRequest(t, router, http.MethodGet, "/items", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get(TotalCountHeader); got != "1" {
		t.Errorf("%s = %q, want %q", TotalCountHeader, got, "1")
	}

	items := decodeItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (body = %s)", len(items), rec.Body.String())
	}
	got := items[0]
	if got.ID == 0 {
		t.Error("id = 0, want the id the legacy row was inserted with")
	}
	if got.Name != "legacy" || got.Description != "from before" || got.Quantity != 5 {
		t.Errorf("item = %+v, want name %q, description %q, quantity %d", got, "legacy", "from before", 5)
	}
	if got.CategoryID != nil {
		t.Errorf("category_id = %d, want null", *got.CategoryID)
	}

	// The wire shape is part of the contract: the column was added and is reported as JSON null.
	var body []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding raw body %q: %v", rec.Body.String(), err)
	}
	if len(body) != 1 {
		t.Fatalf("body = %s, want exactly one item", rec.Body.String())
	}
	raw, ok := body[0]["category_id"]
	if !ok {
		t.Fatalf("body = %s, want a category_id key", rec.Body.String())
	}
	if string(raw) != "null" {
		t.Errorf("category_id = %s, want null", raw)
	}

	// The migrated row is a normal item: it can still be read by id, with its columns intact.
	read := doRequest(t, router, http.MethodGet, itemPath(got.ID), "")
	if read.Code != http.StatusOK {
		t.Fatalf("reading the migrated item: status = %d, body = %s", read.Code, read.Body.String())
	}
	requireItem(t, decodeItem(t, read), Item{ID: got.ID, Name: "legacy", Description: "from before", Quantity: 5})
}
