package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/quantran-code/lang-pack-crud-go/internal/database"
)

// adjustPath builds the path of one item's adjustment, the way a client would ask for it.
func adjustPath(id uint) string {
	return "/items/" + strconv.FormatUint(uint64(id), 10) + "/adjust"
}

// movementsPath builds the path of one item's movement history.
func movementsPath(id uint) string {
	return "/items/" + strconv.FormatUint(uint64(id), 10) + "/movements"
}

// decodeAdjustResponse decodes a response body into an adjustResponse, so a changed JSON shape
// fails the test.
func decodeAdjustResponse(t *testing.T, rec *httptest.ResponseRecorder) adjustResponse {
	t.Helper()
	var resp adjustResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding adjustment from %q: %v", rec.Body.String(), err)
	}
	return resp
}

// decodeMovements decodes a response body into a slice of StockMovements.
func decodeMovements(t *testing.T, rec *httptest.ResponseRecorder) []StockMovement {
	t.Helper()
	var movements []StockMovement
	if err := json.Unmarshal(rec.Body.Bytes(), &movements); err != nil {
		t.Fatalf("decoding movements from %q: %v", rec.Body.String(), err)
	}
	return movements
}

// mustAdjustItem adjusts one item through the API, so a test can start from a known history. The
// body is built with json.Marshal, and an empty reason is left out of it so the omitted-reason case
// is really omitted rather than sent as "".
func mustAdjustItem(t *testing.T, router *gin.Engine, itemID uint, delta int, reason string) adjustResponse {
	t.Helper()
	payload, err := json.Marshal(struct {
		Delta  int    `json:"delta"`
		Reason string `json:"reason,omitempty"`
	}{Delta: delta, Reason: reason})
	if err != nil {
		t.Fatalf("encoding adjust body: %v", err)
	}
	rec := doRequest(t, router, http.MethodPost, adjustPath(itemID), string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("adjusting item: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return decodeAdjustResponse(t, rec)
}

// adjustStep is one adjustment a test applies to the seeded item, in order.
type adjustStep struct {
	delta  int
	reason string
}

// wantMovement is the movement one adjustment is expected to record.
type wantMovement struct {
	delta         int
	reason        string
	quantityAfter int
}

// requireMovement compares one movement with the values a case expects for it: the delta, the reason
// and the quantity after, all belonging to itemID and carrying the id the insert generated.
func requireMovement(t *testing.T, got StockMovement, want wantMovement, itemID uint) {
	t.Helper()
	if got.ID == 0 {
		t.Error("movement id = 0, want a generated id")
	}
	if got.ItemID != itemID {
		t.Errorf("item_id = %d, want %d", got.ItemID, itemID)
	}
	if got.Delta != want.delta {
		t.Errorf("delta = %d, want %d", got.Delta, want.delta)
	}
	if got.Reason != want.reason {
		t.Errorf("reason = %q, want %q", got.Reason, want.reason)
	}
	if got.QuantityAfter != want.quantityAfter {
		t.Errorf("quantity_after = %d, want %d", got.QuantityAfter, want.quantityAfter)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at is the zero time, want the insert time")
	}
}

// requireMovementWireShape asserts every movement in a movements response carries exactly the six
// documented keys and that created_at is a non-empty RFC3339 timestamp string rather than a number
// or the zero time.
func requireMovementWireShape(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	var body []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding raw movements from %q: %v", rec.Body.String(), err)
	}

	wantKeys := []string{"id", "item_id", "delta", "reason", "quantity_after", "created_at"}
	for _, entry := range body {
		if len(entry) != len(wantKeys) {
			t.Fatalf("movement = %v, want exactly the keys %v", entry, wantKeys)
		}
		for _, key := range wantKeys {
			if _, ok := entry[key]; !ok {
				t.Fatalf("movement = %v, want a %q key", entry, key)
			}
		}

		var text string
		if err := json.Unmarshal(entry["created_at"], &text); err != nil {
			t.Fatalf("created_at = %s, want a JSON string: %v", entry["created_at"], err)
		}
		parsed, err := time.Parse(time.RFC3339, text)
		if err != nil {
			t.Errorf("created_at = %q, want an RFC3339 timestamp: %v", text, err)
			continue
		}
		if parsed.IsZero() {
			t.Errorf("created_at = %q, want the insert time, not the zero time", text)
		}
	}
}

// requireNoMovements asserts GET /items/:id/movements answered 200 with an empty array.
func requireNoMovements(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("listing movements: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(TotalCountHeader); got != "" {
		t.Errorf("%s = %q, want no header", TotalCountHeader, got)
	}
	if got := rec.Body.String(); got != "[]" {
		t.Errorf("movements = %s, want []", got)
	}
}

func TestAdjustItem(t *testing.T) {
	// Every case seeds one item with startQuantity, then applies steps in order through the API.
	// want is the history the item must hold afterwards in creation order, so the newest-first read
	// is checked by walking it backwards; wantQuantity is the stored quantity at the end.
	tests := []struct {
		name          string
		startQuantity int
		steps         []adjustStep
		want          []wantMovement
		wantQuantity  int
	}{
		{
			name:          "positive delta restocks",
			startQuantity: 10,
			steps:         []adjustStep{{delta: 5, reason: "restock"}},
			want:          []wantMovement{{delta: 5, reason: "restock", quantityAfter: 15}},
			wantQuantity:  15,
		},
		{
			name:          "negative delta that reaches zero",
			startQuantity: 10,
			steps:         []adjustStep{{delta: -10, reason: "sold"}},
			want:          []wantMovement{{delta: -10, reason: "sold", quantityAfter: 0}},
			wantQuantity:  0,
		},
		{
			name:          "omitted reason is stored empty",
			startQuantity: 4,
			steps:         []adjustStep{{delta: -1}},
			want:          []wantMovement{{delta: -1, reason: "", quantityAfter: 3}},
			wantQuantity:  3,
		},
		{
			name:          "two adjustments chain their quantity_after",
			startQuantity: 10,
			steps: []adjustStep{
				{delta: 5, reason: "restock"},
				{delta: -12, reason: "sold"},
			},
			want: []wantMovement{
				{delta: 5, reason: "restock", quantityAfter: 15},
				{delta: -12, reason: "sold", quantityAfter: 3},
			},
			wantQuantity: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			item := mustCreateItem(t, router, "widget", "a thing", tc.startQuantity)

			for i, step := range tc.steps {
				resp := mustAdjustItem(t, router, item.ID, step.delta, step.reason)

				want := tc.want[i]
				requireItem(t, resp.Item, Item{
					ID:          item.ID,
					Name:        "widget",
					Description: "a thing",
					Quantity:    want.quantityAfter,
				})
				requireMovement(t, resp.Movement, want, item.ID)
			}

			// The stored row, not just the response, must carry the final quantity.
			got := doRequest(t, router, http.MethodGet, itemPath(item.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading item back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireItem(t, decodeItem(t, got), Item{
				ID:          item.ID,
				Name:        "widget",
				Description: "a thing",
				Quantity:    tc.wantQuantity,
			})

			// The history holds exactly one movement per step, newest first.
			rec := doRequest(t, router, http.MethodGet, movementsPath(item.ID), "")
			if rec.Code != http.StatusOK {
				t.Fatalf("listing movements: status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(TotalCountHeader); got != "" {
				t.Errorf("%s = %q, want no header", TotalCountHeader, got)
			}
			requireMovementWireShape(t, rec)

			movements := decodeMovements(t, rec)
			if len(movements) != len(tc.want) {
				t.Fatalf("got %d movements, want %d (body = %s)", len(movements), len(tc.want), rec.Body.String())
			}
			for i, movement := range movements {
				// Newest first: the row at position i is the last step minus i.
				requireMovement(t, movement, tc.want[len(tc.want)-1-i], item.ID)
				if i > 0 && movements[i].ID >= movements[i-1].ID {
					t.Errorf("movement ids = %d, %d, want descending", movements[i-1].ID, movements[i].ID)
				}
			}
		})
	}
}

func TestAdjustItemValidation(t *testing.T) {
	// Every case runs against one stored item of quantity 0, so the negative-stock case is really
	// insufficient. wantRaw is the exact body the rejected request must produce; every rejected
	// request must leave the item's quantity at 0 and its history empty.
	tests := []struct {
		name       string
		path       func(id uint) string
		body       string
		wantStatus int
		wantRaw    string
	}{
		{
			name:       "missing delta",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"delta is required"}`,
		},
		{
			name:       "null delta",
			body:       `{"delta":null}`,
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"delta is required"}`,
		},
		{
			name:       "zero delta",
			body:       `{"delta":0}`,
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"delta must be non-zero"}`,
		},
		{
			name:       "fractional delta",
			body:       `{"delta":1.5}`,
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"invalid JSON body"}`,
		},
		{
			name:       "delta sent as a string",
			body:       `{"delta":"5"}`,
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"invalid JSON body"}`,
		},
		{
			name:       "delta that would make the quantity negative",
			body:       `{"delta":-1,"reason":"sold"}`,
			wantStatus: http.StatusConflict,
			wantRaw:    `{"error":"insufficient stock"}`,
		},
		{
			name:       "missing item wins over an invalid body",
			path:       fixedPath("/items/999/adjust"),
			body:       `{}`,
			wantStatus: http.StatusNotFound,
			wantRaw:    `{"error":"item not found"}`,
		},
		{
			name:       "non-numeric id",
			path:       fixedPath("/items/abc/adjust"),
			body:       `{"delta":1}`,
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"invalid item id"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			item := mustCreateItem(t, router, "widget", "a thing", 0)

			path := adjustPath(item.ID)
			if tc.path != nil {
				path = tc.path(item.ID)
			}

			rec := doRequest(t, router, http.MethodPost, path, tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			requireError(t, rec)
			if got := rec.Body.String(); got != tc.wantRaw {
				t.Errorf("body = %s, want %s", got, tc.wantRaw)
			}

			// A rejected adjustment must not have changed the quantity or recorded anything.
			got := doRequest(t, router, http.MethodGet, itemPath(item.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading item back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireItem(t, decodeItem(t, got), Item{
				ID:          item.ID,
				Name:        "widget",
				Description: "a thing",
				Quantity:    0,
			})

			requireNoMovements(t, doRequest(t, router, http.MethodGet, movementsPath(item.ID), ""))
		})
	}
}

func TestListItemMovements(t *testing.T) {
	// seed returns the id the request queries, after seeding whatever rows the case needs. wantBody
	// is the exact body an empty or absent history must produce; check inspects the decoded rows of
	// a 200 with movements.
	tests := []struct {
		name       string
		seed       func(t *testing.T, router *gin.Engine) uint
		path       func(id uint) string
		wantStatus int
		wantBody   string
		wantRaw    string
		check      func(t *testing.T, id uint, got []StockMovement)
	}{
		{
			name: "returns every movement newest first",
			seed: func(t *testing.T, router *gin.Engine) uint {
				t.Helper()
				item := mustCreateItem(t, router, "widget", "a thing", 0)
				mustAdjustItem(t, router, item.ID, 1, "first")
				mustAdjustItem(t, router, item.ID, 2, "second")
				mustAdjustItem(t, router, item.ID, 3, "third")
				return item.ID
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, id uint, got []StockMovement) {
				t.Helper()
				if len(got) != 3 {
					t.Fatalf("got %d movements, want 3", len(got))
				}
				wantDeltas := []int{3, 2, 1}
				wantReasons := []string{"third", "second", "first"}
				wantQuantities := []int{6, 3, 1}
				for i, movement := range got {
					if movement.Delta != wantDeltas[i] {
						t.Errorf("row %d delta = %d, want %d", i, movement.Delta, wantDeltas[i])
					}
					if movement.Reason != wantReasons[i] {
						t.Errorf("row %d reason = %q, want %q", i, movement.Reason, wantReasons[i])
					}
					if movement.QuantityAfter != wantQuantities[i] {
						t.Errorf("row %d quantity_after = %d, want %d", i, movement.QuantityAfter, wantQuantities[i])
					}
					if movement.ItemID != id {
						t.Errorf("row %d item_id = %d, want %d", i, movement.ItemID, id)
					}
					if i > 0 && got[i].ID >= got[i-1].ID {
						t.Errorf("row %d id = %d, want it below the previous %d", i, got[i].ID, got[i-1].ID)
					}
				}
			},
		},
		{
			name: "item with no movements is an empty array",
			seed: func(t *testing.T, router *gin.Engine) uint {
				t.Helper()
				return mustCreateItem(t, router, "widget", "a thing", 3).ID
			},
			wantStatus: http.StatusOK,
			wantBody:   "[]",
		},
		{
			name: "missing item",
			seed: func(t *testing.T, router *gin.Engine) uint {
				t.Helper()
				other := mustCreateItem(t, router, "other", "a thing", 1)
				mustAdjustItem(t, router, other.ID, 1, "other")
				return other.ID
			},
			path:       fixedPath("/items/999/movements"),
			wantStatus: http.StatusNotFound,
			wantRaw:    `{"error":"item not found"}`,
		},
		{
			name: "non-numeric id",
			seed: func(t *testing.T, router *gin.Engine) uint {
				t.Helper()
				return mustCreateItem(t, router, "widget", "a thing", 1).ID
			},
			path:       fixedPath("/items/abc/movements"),
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"invalid item id"}`,
		},
		{
			name: "another item's movements are never included",
			seed: func(t *testing.T, router *gin.Engine) uint {
				t.Helper()
				target := mustCreateItem(t, router, "target", "a thing", 0)
				other := mustCreateItem(t, router, "other", "a thing", 0)
				mustAdjustItem(t, router, target.ID, 2, "target")
				mustAdjustItem(t, router, other.ID, 7, "other")
				mustAdjustItem(t, router, other.ID, 3, "other")
				return target.ID
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, id uint, got []StockMovement) {
				t.Helper()
				if len(got) != 1 {
					t.Fatalf("got %d movements, want only the one for item %d", len(got), id)
				}
				requireMovement(t, got[0], wantMovement{delta: 2, reason: "target", quantityAfter: 2}, id)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			id := tc.seed(t, router)

			path := movementsPath(id)
			if tc.path != nil {
				path = tc.path(id)
			}

			rec := doRequest(t, router, http.MethodGet, path, "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get(TotalCountHeader); got != "" {
				t.Errorf("%s = %q, want no header", TotalCountHeader, got)
			}

			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				if got := rec.Body.String(); got != tc.wantRaw {
					t.Errorf("body = %s, want %s", got, tc.wantRaw)
				}
				return
			}

			if tc.wantBody != "" {
				requireMovementWireShape(t, rec)
				if got := rec.Body.String(); got != tc.wantBody {
					t.Fatalf("body = %s, want %s", got, tc.wantBody)
				}
				return
			}

			requireMovementWireShape(t, rec)
			if tc.check != nil {
				tc.check(t, id, decodeMovements(t, rec))
			}
		})
	}
}

func TestItemWritesCreateNoMovement(t *testing.T) {
	router := newTestRouter(t)

	item := mustCreateItem(t, router, "widget", "a thing", 3)

	// A create sets the quantity directly and must leave no audit row behind.
	requireNoMovements(t, doRequest(t, router, http.MethodGet, movementsPath(item.ID), ""))

	rec := doRequest(t, router, http.MethodPut, itemPath(item.ID), `{"name":"widget","description":"a thing","quantity":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("updating item: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	requireItem(t, decodeItem(t, rec), Item{
		ID:          item.ID,
		Name:        "widget",
		Description: "a thing",
		Quantity:    8,
	})

	// A PUT also sets the quantity directly, so the history is still empty.
	requireNoMovements(t, doRequest(t, router, http.MethodGet, movementsPath(item.ID), ""))
}

func TestAdjustItemRollsBackWhenMovementInsertFails(t *testing.T) {
	db, err := database.Open(database.NewMemoryDSN())
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}

	// Building the router over this database is what runs the AutoMigrate that creates
	// stock_movements, so the table exists before the test drops it again.
	router := newTestRouterWithDB(t, db)
	item := mustCreateItem(t, router, "widget", "a thing", 7)

	if err := db.Migrator().DropTable(&StockMovement{}); err != nil {
		t.Fatalf("dropping the stock_movements table: %v", err)
	}

	rec := doRequest(t, router, http.MethodPost, adjustPath(item.ID), `{"delta":-3,"reason":"lost"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	requireError(t, rec)
	if got := errorMessage(t, rec); got != "adjusting item" {
		t.Errorf("error message = %q, want %q", got, "adjusting item")
	}

	// The quantity write and the movement insert are one transaction, so the failed insert must
	// have rolled the quantity back.
	got := doRequest(t, router, http.MethodGet, itemPath(item.ID), "")
	if got.Code != http.StatusOK {
		t.Fatalf("reading item back: status = %d, body = %s", got.Code, got.Body.String())
	}
	requireItem(t, decodeItem(t, got), Item{
		ID:          item.ID,
		Name:        "widget",
		Description: "a thing",
		Quantity:    7,
	})
}
