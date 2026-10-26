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

// Category is the resource served by the category endpoints and the table GORM migrates. The
// name carries a unique index, but the duplicate check that produces the 409 runs in code first;
// the index is only a backstop.
type Category struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"uniqueIndex" json:"name"`
}

// categoryRequest is the body accepted by create and rename. Name is stored exactly as sent; only
// the blank check trims, so "Work" and "Work " are two different names.
type categoryRequest struct {
	Name string `json:"name"`
}

// validate applies the rule shared by create and rename: a non-blank name. The stored value is
// not trimmed.
func (r categoryRequest) validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("name is required")
	}
	return nil
}

// duplicateCategoryNameError builds the message every duplicate name produces.
func duplicateCategoryNameError() error {
	return errors.New("category name already exists")
}

// categoryNameTaken reports whether a category already stores name exactly. A non-zero excludeID
// is left out of the count, so a rename to a category's own current name is not a conflict.
func categoryNameTaken(db *gorm.DB, name string, excludeID uint) (bool, error) {
	query := db.Model(&Category{}).Where("name = ?", name)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, fmt.Errorf("counting categories named %q: %w", name, err)
	}
	return count > 0, nil
}

// createCategory handles POST /categories. A blank name is a 400 and a name another category
// already stores exactly is a 409, both decided before anything is written.
func createCategory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req categoryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := req.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		ctx := c.Request.Context()
		taken, err := categoryNameTaken(db.WithContext(ctx), req.Name, 0)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "creating category")
			return
		}
		if taken {
			writeError(c, http.StatusConflict, duplicateCategoryNameError().Error())
			return
		}

		category := Category{Name: req.Name}
		if err := db.WithContext(ctx).Create(&category).Error; err != nil {
			writeError(c, http.StatusInternalServerError, "creating category")
			return
		}

		c.JSON(http.StatusCreated, category)
	}
}

// listCategories handles GET /categories. The query string is parsed and validated before the
// database is touched, then one page is loaded through the shared paging helper, which also
// reports how many rows matched before limit and offset: that total is the X-Total-Count header.
// The slice is made up front so an empty result marshals as [] instead of null.
func listCategories(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		params, err := parseListParams(c, categoryListSpec)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		categories := make([]Category, 0)
		total, err := listPage(func() *gorm.DB {
			return db.WithContext(c.Request.Context())
		}, params, &categories)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "listing categories")
			return
		}

		c.Header(TotalCountHeader, strconv.FormatInt(total, 10))
		c.JSON(http.StatusOK, categories)
	}
}

// updateCategory handles PUT /categories/:id as a full replacement of the single name column. The
// id is resolved and the row loaded before the body is looked at, so a missing category is a 404
// even when the body would also fail validation. A blank or missing name is the same 400 create
// gives, and a name another category already stores exactly is a 409; renaming a category to its
// own current name is not a conflict, because the pre-check leaves this row out of the count.
func updateCategory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid category id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "category not found")
			return
		}

		ctx := c.Request.Context()
		var category Category
		err = db.WithContext(ctx).First(&category, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "category not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "loading category")
			return
		}

		var req categoryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := req.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		taken, err := categoryNameTaken(db.WithContext(ctx), req.Name, uint(id))
		if err != nil {
			writeError(c, http.StatusInternalServerError, "updating category")
			return
		}
		if taken {
			writeError(c, http.StatusConflict, duplicateCategoryNameError().Error())
			return
		}

		err = db.WithContext(ctx).Model(&Category{}).Where("id = ?", id).Update("name", req.Name).Error
		if err != nil {
			writeError(c, http.StatusInternalServerError, "updating category")
			return
		}

		if err := db.WithContext(ctx).First(&category, id).Error; err != nil {
			writeError(c, http.StatusInternalServerError, "loading category")
			return
		}

		c.JSON(http.StatusOK, category)
	}
}

// getCategory handles GET /categories/:id. A non-numeric id is a 400, a missing row a 404.
func getCategory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid category id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "category not found")
			return
		}

		var category Category
		err = db.WithContext(c.Request.Context()).First(&category, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "category not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "loading category")
			return
		}

		c.JSON(http.StatusOK, category)
	}
}

// deleteCategory handles DELETE /categories/:id. SQLite foreign-key enforcement is off, so the
// reference check is written in code: the category is loaded first, which makes a missing id a 404
// and never a 409, then the items pointing at it are counted and a non-zero count is a 409 that
// leaves both the category and its items alone. RowsAffected tells a real delete (204) from a row
// that vanished between the load and the delete (404).
func deleteCategory(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid category id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "category not found")
			return
		}

		ctx := c.Request.Context()
		var category Category
		err = db.WithContext(ctx).First(&category, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "category not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "loading category")
			return
		}

		var referenced int64
		if err := db.WithContext(ctx).Model(&Item{}).Where("category_id = ?", id).Count(&referenced).Error; err != nil {
			writeError(c, http.StatusInternalServerError, "deleting category")
			return
		}
		if referenced > 0 {
			writeError(c, http.StatusConflict, "category has items")
			return
		}

		result := db.WithContext(ctx).Delete(&Category{}, id)
		if result.Error != nil {
			writeError(c, http.StatusInternalServerError, "deleting category")
			return
		}
		if result.RowsAffected == 0 {
			writeError(c, http.StatusNotFound, "category not found")
			return
		}

		c.Status(http.StatusNoContent)
	}
}
