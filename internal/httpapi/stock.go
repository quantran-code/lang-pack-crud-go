package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StockMovement is the audit row written by every adjustment: the relative change applied to an
// item's quantity, the optional reason the client gave, and the quantity the item holds after the
// change. CreatedAt is filled by GORM's autoCreateTime when the row is inserted and marshals as an
// RFC3339 string. ItemID carries an index because GET /items/:id/movements looks movements up by
// item; SQLite foreign-key enforcement is off in this project, so it is a bare reference, not a
// constraint.
type StockMovement struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	ItemID        uint      `gorm:"index" json:"item_id"`
	Delta         int       `json:"delta"`
	Reason        string    `json:"reason"`
	QuantityAfter int       `json:"quantity_after"`
	CreatedAt     time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// adjustRequest is the body accepted by the adjustment. Delta is a pointer so an absent key and an
// explicit null are both nil, which is the "delta is required" case, while an explicit 0 is a
// distinct non-nil value that is rejected with its own message. A value that is not an integer at
// all never reaches this struct: ShouldBindJSON fails and the handler answers "invalid JSON body".
// Reason is optional and is stored exactly as sent; an omitted reason is "".
type adjustRequest struct {
	Delta  *int   `json:"delta"`
	Reason string `json:"reason"`
}

// validate applies the rule the adjustment body has: a present, non-zero delta. A negative delta
// is valid here; whether the item holds enough stock for it is decided by the conditional UPDATE
// in the transaction, not by this check.
func (r adjustRequest) validate() error {
	if r.Delta == nil {
		return errors.New("delta is required")
	}
	if *r.Delta == 0 {
		return errors.New("delta must be non-zero")
	}
	return nil
}

// adjustResponse is the body a successful adjustment returns: the item as it now stands and the
// movement that was recorded for it.
type adjustResponse struct {
	Item     Item          `json:"item"`
	Movement StockMovement `json:"movement"`
}

// errInsufficientStock is returned from the adjustment transaction when the conditional UPDATE
// matched no row because the delta would take the quantity below zero. The handler maps it to a
// 409 whose message is its Error() text; matching is done with errors.Is, so the transaction can
// reuse the one value.
var errInsufficientStock = errors.New("insufficient stock")

// stockAdjustmentMu serialises the whole adjust request: the item load that decides 404, and the
// transaction that writes the new quantity and the movement. This project runs SQLite with
// cache=shared and no busy timeout, and in shared-cache mode a second connection that touches a
// table another connection has write-locked gets SQLITE_LOCKED straight away instead of waiting,
// so two overlapping adjustments would surface a 5xx to one of them. Holding one lock across the
// read and the write is what keeps a burst of adjustments free of lock errors. It is not what
// keeps stock non-negative — the UPDATE's own quantity + delta >= 0 condition is — and it is
// deliberately coarse: every adjustment, on every item, queues behind this one mutex.
var stockAdjustmentMu sync.Mutex

// adjustItem handles POST /items/:id/adjust. The id is resolved and the row loaded before the body
// is looked at, so a missing item is a 404 even when the body would also fail validation. A body
// without a delta, or with a delta of 0, is a 400; a delta that would make the quantity negative is
// a 409 that writes nothing.
//
// The quantity write and the movement insert happen in one transaction. Its first statement is the
// conditional UPDATE, so the write lock is taken before any read inside the transaction and the
// guard rides in the WHERE clause: quantity = quantity + ? only matches while quantity + ? >= 0, so
// the stored row can never go negative no matter what the pre-load saw. No statement in the
// transaction writes the quantity unconditionally. Zero rows affected means the condition failed,
// which is re-decided against the row as it now stands: a row that is gone is a 404, anything else
// is the 409. On success the item is reloaded inside the transaction so the movement's
// quantity_after and the response both carry the committed value, and the movement is inserted
// before the transaction commits; any error rolls both writes back, leaving the quantity untouched.
func adjustItem(db *gorm.DB) gin.HandlerFunc {
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

		stockAdjustmentMu.Lock()
		defer stockAdjustmentMu.Unlock()

		ctx := c.Request.Context()
		var item Item
		err = db.WithContext(ctx).First(&item, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "item not found")
			return
		}
		if err != nil {
			writeError(c, http.StatusInternalServerError, "adjusting item")
			return
		}

		var req adjustRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := req.validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		var movement StockMovement
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Model(&Item{}).
				Where("id = ? AND quantity + ? >= 0", id, *req.Delta).
				Update("quantity", gorm.Expr("quantity + ?", *req.Delta))
			if result.Error != nil {
				return fmt.Errorf("applying stock adjustment: %w", result.Error)
			}

			if result.RowsAffected == 0 {
				// The condition matched nothing. Reload the row to tell a vanished item (404) from
				// one that simply cannot go that low (409).
				var current Item
				if err := tx.First(&current, id).Error; err != nil {
					return err
				}
				return errInsufficientStock
			}

			var adjusted Item
			if err := tx.First(&adjusted, id).Error; err != nil {
				return fmt.Errorf("loading adjusted item: %w", err)
			}
			item = adjusted

			movement = StockMovement{
				ItemID:        item.ID,
				Delta:         *req.Delta,
				Reason:        req.Reason,
				QuantityAfter: item.Quantity,
			}
			if err := tx.Create(&movement).Error; err != nil {
				return fmt.Errorf("recording stock movement: %w", err)
			}

			return nil
		})
		if err != nil {
			if errors.Is(err, errInsufficientStock) {
				writeError(c, http.StatusConflict, errInsufficientStock.Error())
				return
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeError(c, http.StatusNotFound, "item not found")
				return
			}
			writeError(c, http.StatusInternalServerError, "adjusting item")
			return
		}

		c.JSON(http.StatusOK, adjustResponse{Item: item, Movement: movement})
	}
}

// listItemMovements handles GET /items/:id/movements. The id is resolved and the row loaded before
// any movement is looked at, so a non-numeric id is a 400, a missing item a 404 and never an empty
// array. The whole history of that one item is returned, newest first: the list is ordered by id
// descending and has no limit or offset, and there is no X-Total-Count header, because this is not
// a paged collection. The slice is made up front so an item with no movements marshals as []
// instead of null. No lock is taken: this handler only reads.
func listItemMovements(db *gorm.DB) gin.HandlerFunc {
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
			writeError(c, http.StatusInternalServerError, "listing movements")
			return
		}

		movements := make([]StockMovement, 0)
		err = db.WithContext(ctx).
			Where("item_id = ?", id).
			Order("id DESC").
			Find(&movements).Error
		if err != nil {
			writeError(c, http.StatusInternalServerError, "listing movements")
			return
		}

		c.JSON(http.StatusOK, movements)
	}
}
