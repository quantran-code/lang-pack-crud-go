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

// Status is the closed vocabulary of task states. It is a named string type so the
// database column stays a plain string while Go code can only use the declared values.
type Status string

// The allowed task statuses. These three constants are the only place the wire values
// are written down; every handler, filter and validation uses them by name.
const (
	StatusOpen       Status = "OPEN"
	StatusInProgress Status = "IN_PROGRESS"
	StatusDone       Status = "DONE"
)

// Valid reports whether s is one of the declared statuses. The zero value and any
// other string are invalid.
func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusDone:
		return true
	default:
		return false
	}
}

// Task is the resource served by the task endpoints and the table GORM migrates.
type Task struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
}

// taskRequest is the body accepted by create. Status is a pointer so an omitted key and an
// explicit null are both nil, while an empty string is a non-nil invalid value. A nil status
// means the task takes the default, StatusOpen.
type taskRequest struct {
	Title  string  `json:"title"`
	Status *Status `json:"status"`
}

// validate applies the rule shared by create: a non-blank title and, when the status is
// present, a value from the closed vocabulary. A nil status is valid here; createTask
// supplies the default.
func (r taskRequest) validate() error {
	if strings.TrimSpace(r.Title) == "" {
		return errors.New("title is required")
	}
	if r.Status != nil && !r.Status.Valid() {
		return invalidStatusError()
	}
	return nil
}

// invalidStatusError builds the single message every invalid status produces, from the
// constants, so no handler repeats the wire values.
func invalidStatusError() error {
	return fmt.Errorf("status must be one of %s, %s, %s", StatusOpen, StatusInProgress, StatusDone)
}

// createTask handles POST /tasks. An omitted or null status defaults to StatusOpen.
func createTask(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req taskRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := req.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		task := Task{
			Title:  req.Title,
			Status: StatusOpen,
		}
		if req.Status != nil {
			task.Status = *req.Status
		}
		if err := db.WithContext(c.Request.Context()).Create(&task).Error; err != nil {
			writeError(c, http.StatusInternalServerError, "creating task")
			return
		}

		c.JSON(http.StatusCreated, task)
	}
}

// listTasks handles GET /tasks. The query string is parsed and validated before the database is
// touched. The filter is read with GetQuery so an absent parameter can be told apart from a
// present but empty one, which is an invalid status; it is resolved once, so the count and the page
// see exactly the same filter. One page is then loaded through the shared paging helper, which also
// reports how many rows matched the filter before limit and offset: that total is the X-Total-Count
// header. The slice is made up front so an empty result marshals as [] instead of null.
func listTasks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		params, err := parseListParams(c, taskListSpec)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		var status *Status
		if raw, ok := c.GetQuery("status"); ok {
			parsed := Status(raw)
			if !parsed.Valid() {
				writeError(c, http.StatusBadRequest, invalidStatusError().Error())
				return
			}
			status = &parsed
		}

		tasks := make([]Task, 0)
		total, err := listPage(func() *gorm.DB {
			query := db.WithContext(c.Request.Context())
			if status != nil {
				query = query.Where("status = ?", string(*status))
			}
			return query
		}, params, &tasks)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "listing tasks")
			return
		}

		c.Header(TotalCountHeader, strconv.FormatInt(total, 10))
		c.JSON(http.StatusOK, tasks)
	}
}

// statusRequest is the body accepted by the status change. Status is a pointer so a missing
// key and an explicit null are both nil, and both are rejected: the status is required here.
type statusRequest struct {
	Status *Status `json:"status"`
}

// getTask handles GET /tasks/:id. A non-numeric id is a 400, a missing row a 404.
func getTask(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid task id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "task not found")
			return
		}

		var task Task
		err = db.WithContext(c.Request.Context()).First(&task, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "task not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "loading task")
			return
		}

		c.JSON(http.StatusOK, task)
	}
}

// updateTaskStatus handles PATCH /tasks/:id/status. The id is resolved and the row loaded
// before the body is looked at, so a missing task is a 404 even when the body would also fail
// validation. An absent, null, empty or unknown status is the same 400.
func updateTaskStatus(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok, err := parseID(c.Param("id"))
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid task id")
			return
		}
		if !ok {
			writeError(c, http.StatusNotFound, "task not found")
			return
		}

		ctx := c.Request.Context()
		var task Task
		err = db.WithContext(ctx).First(&task, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "task not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "loading task")
			return
		}

		var req statusRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Status == nil || !req.Status.Valid() {
			writeError(c, http.StatusBadRequest, invalidStatusError().Error())
			return
		}

		err = db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).Update("status", *req.Status).Error
		if err != nil {
			writeError(c, http.StatusInternalServerError, "updating task")
			return
		}

		if err := db.WithContext(ctx).First(&task, id).Error; err != nil {
			writeError(c, http.StatusInternalServerError, "loading task")
			return
		}

		c.JSON(http.StatusOK, task)
	}
}
