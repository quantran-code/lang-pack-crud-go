package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Item is the resource served by the CRUD endpoints and the table GORM migrates. CategoryID is a
// bare nullable reference, null when the item has no category: SQLite foreign-key enforcement is
// off, so the link is checked in code, not by a constraint.
type Item struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Quantity    int    `json:"quantity"`
	CategoryID  *uint  `gorm:"index" json:"category_id"`
}

// itemRequest is the body accepted by create and update. Quantity is a pointer so an absent
// quantity (nil) can be told apart from an explicit 0, which is a valid value. CategoryID is a
// pointer for the same reason: an omitted key and an explicit null are both nil, which means no
// category on create and a cleared reference on update.
type itemRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Quantity    *int   `json:"quantity"`
	CategoryID  *uint  `json:"category_id"`
}

// validate applies the rules shared by create and update: a non-blank name and a quantity that
// is present and not negative. A quantity of 0 is valid.
func (r itemRequest) validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("name is required")
	}
	if r.Quantity == nil {
		return errors.New("quantity is required")
	}
	if *r.Quantity < 0 {
		return errors.New("quantity must be zero or greater")
	}
	return nil
}

// categoryExists reports whether a category with this id is stored. The raw error is returned, so a
// database failure becomes a 500 rather than the 400 a reference to a missing category produces.
func categoryExists(db *gorm.DB, id uint) (bool, error) {
	var count int64
	if err := db.Model(&Category{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, fmt.Errorf("counting categories with id %d: %w", id, err)
	}
	return count > 0, nil
}

// writeError writes the error shape every failing handler uses: {"error": "<message>"}.
func writeError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}

// createItem handles POST /items.
func createItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req itemRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := req.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		ctx := c.Request.Context()
		if req.CategoryID != nil {
			exists, err := categoryExists(db.WithContext(ctx), *req.CategoryID)
			if err != nil {
				writeError(c, http.StatusInternalServerError, "creating item")
				return
			}
			if !exists {
				writeError(c, http.StatusBadRequest, "category does not exist")
				return
			}
		}

		item := Item{
			Name:        req.Name,
			Description: req.Description,
			Quantity:    *req.Quantity,
			CategoryID:  req.CategoryID,
		}
		if err := db.WithContext(ctx).Create(&item).Error; err != nil {
			writeError(c, http.StatusInternalServerError, "creating item")
			return
		}

		c.JSON(http.StatusCreated, item)
	}
}

// listItems handles GET /items. The query string is parsed and validated before the database is
// touched, so a bad limit, offset, sort, order or category_id is a 400 that never reaches SQL. The
// category_id filter is resolved once, so the count and the page see exactly the same filter; a
// well-formed id that no row can carry matches nothing and is answered without a query. One page is
// then loaded through the shared paging helper, which also reports how many rows matched before
// limit and offset: that total is the X-Total-Count header. The slice is made up front so an empty
// result marshals as [] instead of null.
func listItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		params, err := parseListParams(c, itemListSpec)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		ctx := c.Request.Context()
		items := make([]Item, 0)

		var categoryID uint64
		filterByCategory := false
		if raw, ok := c.GetQuery("category_id"); ok {
			id, found, err := parseID(raw)
			if err != nil {
				writeError(c, http.StatusBadRequest, "category_id must be a number")
				return
			}
			if !found {
				c.Header(TotalCountHeader, "0")
				c.JSON(http.StatusOK, items)
				return
			}
			categoryID = id
			filterByCategory = true
		}

		total, err := listPage(func() *gorm.DB {
			query := db.WithContext(ctx)
			if filterByCategory {
				query = query.Where("category_id = ?", categoryID)
			}
			return query
		}, params, &items)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "listing items")
			return
		}

		c.Header(TotalCountHeader, strconv.FormatInt(total, 10))
		c.JSON(http.StatusOK, items)
	}
}

// getItem handles GET /items/:id. A non-numeric id is a 400, a missing row a 404.
func getItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid item id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}

		var item Item
		err = db.WithContext(c.Request.Context()).First(&item, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "loading item")
			return
		}

		c.JSON(http.StatusOK, item)
	}
}

// updateItem handles PUT /items/:id as a full replacement. The id is resolved before the body is
// looked at, so a missing row is a 404 even when the body would also fail validation. The four
// columns are written through a map so an omitted description or a quantity of 0 reaches the
// database, and a nil category_id clears the reference; struct Updates would skip those zero values
// and keep the old ones. An untyped nil interface is put in the map for a cleared reference, since
// a typed nil pointer inside the interface would be written as a value rather than as NULL.
func updateItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid item id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}

		ctx := c.Request.Context()
		var item Item
		err = db.WithContext(ctx).First(&item, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "loading item")
			return
		}

		var req itemRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := req.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		if req.CategoryID != nil {
			exists, err := categoryExists(db.WithContext(ctx), *req.CategoryID)
			if err != nil {
				writeError(c, http.StatusInternalServerError, "updating item")
				return
			}
			if !exists {
				writeError(c, http.StatusBadRequest, "category does not exist")
				return
			}
		}

		var categoryID any
		if req.CategoryID != nil {
			categoryID = *req.CategoryID
		}

		err = db.WithContext(ctx).Model(&Item{}).Where("id = ?", id).Updates(map[string]any{
			"name":        req.Name,
			"description": req.Description,
			"quantity":    *req.Quantity,
			"category_id": categoryID,
		}).Error
		if err != nil {
			writeError(c, http.StatusInternalServerError, "updating item")
			return
		}

		if err := db.WithContext(ctx).First(&item, id).Error; err != nil {
			writeError(c, http.StatusInternalServerError, "loading item")
			return
		}

		c.JSON(http.StatusOK, item)
	}
}

// deleteItem handles DELETE /items/:id. RowsAffected tells a real delete (204) from a missing row
// (404) without a separate existence query.
func deleteItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid item id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}

		result := db.WithContext(c.Request.Context()).Delete(&Item{}, id)
		if result.Error != nil {
			writeError(c, http.StatusInternalServerError, "deleting item")
			return
		}
		if result.RowsAffected == 0 {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}

		c.Status(http.StatusNoContent)
	}
}
