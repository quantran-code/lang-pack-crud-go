package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// doRequest drives the real router and records the response. An empty body means no request body.
func doRequest(t *testing.T, router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// itemPath builds the path of one item, the way a client would ask for it.
func itemPath(id uint) string {
	return "/items/" + strconv.FormatUint(uint64(id), 10)
}

// fixedPath returns a path builder that ignores the seeded ids, for ids that do not exist or are
// not numbers.
func fixedPath(path string) func(uint) string {
	return func(uint) string { return path }
}

// decodeItem decodes a response body into an Item, so a changed JSON shape fails the test.
func decodeItem(t *testing.T, rec *httptest.ResponseRecorder) Item {
	t.Helper()
	var item Item
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decoding item from %q: %v", rec.Body.String(), err)
	}
	return item
}

// decodeItems decodes a response body into a slice of Items.
func decodeItems(t *testing.T, rec *httptest.ResponseRecorder) []Item {
	t.Helper()
	var items []Item
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decoding items from %q: %v", rec.Body.String(), err)
	}
	return items
}

// requireError asserts the response carries the {"error": "<message>"} shape: JSON, exactly one
// key, and a non-empty message.
func requireError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding error body %q: %v", rec.Body.String(), err)
	}
	if len(body) != 1 {
		t.Fatalf("error body = %s, want exactly one key", rec.Body.String())
	}
	raw, ok := body["error"]
	if !ok {
		t.Fatalf("error body = %s, want an error key", rec.Body.String())
	}
	var message string
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("decoding error message from %s: %v", rec.Body.String(), err)
	}
	if message == "" {
		t.Errorf("error body = %s, want a non-empty message", rec.Body.String())
	}
}

// requireItem compares the fields a response is expected to echo back.
func requireItem(t *testing.T, got, want Item) {
	t.Helper()
	if want.ID != 0 && got.ID != want.ID {
		t.Errorf("id = %d, want %d", got.ID, want.ID)
	}
	if got.ID == 0 {
		t.Error("id = 0, want a generated id")
	}
	if got.Name != want.Name {
		t.Errorf("name = %q, want %q", got.Name, want.Name)
	}
	if got.Description != want.Description {
		t.Errorf("description = %q, want %q", got.Description, want.Description)
	}
	if got.Quantity != want.Quantity {
		t.Errorf("quantity = %d, want %d", got.Quantity, want.Quantity)
	}
}

// mustCreateItem creates an item through the API, so a test can start from a known row.
func mustCreateItem(t *testing.T, router *gin.Engine, name, description string, quantity int) Item {
	t.Helper()
	payload, err := json.Marshal(itemRequest{Name: name, Description: description, Quantity: &quantity})
	if err != nil {
		t.Fatalf("encoding create body: %v", err)
	}
	rec := doRequest(t, router, http.MethodPost, "/items", string(payload))
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating item: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return decodeItem(t, rec)
}

func TestCreateItem(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantItem   Item
	}{
		{
			name:       "valid body",
			body:       `{"name":"widget","description":"a thing","quantity":3}`,
			wantStatus: http.StatusCreated,
			wantItem:   Item{Name: "widget", Description: "a thing", Quantity: 3},
		},
		{
			name:       "omitted description defaults to empty",
			body:       `{"name":"bare","quantity":1}`,
			wantStatus: http.StatusCreated,
			wantItem:   Item{Name: "bare", Description: "", Quantity: 1},
		},
		{
			name:       "quantity zero is valid",
			body:       `{"name":"none left","quantity":0}`,
			wantStatus: http.StatusCreated,
			wantItem:   Item{Name: "none left", Quantity: 0},
		},
		{
			name:       "absent name",
			body:       `{"quantity":1}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "blank name",
			body:       `{"name":"   ","quantity":1}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "absent quantity",
			body:       `{"name":"no quantity"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "negative quantity",
			body:       `{"name":"negative","quantity":-1}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			body:       `{"name":`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)

			rec := doRequest(t, router, http.MethodPost, "/items", tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusCreated {
				requireError(t, rec)
				return
			}
			requireItem(t, decodeItem(t, rec), tc.wantItem)
		})
	}
}

func TestListItem(t *testing.T) {
	tests := []struct {
		name     string
		seed     []string
		wantBody string
		want     []Item
	}{
		{
			name:     "empty database is an empty array",
			wantBody: `[]`,
		},
		{
			name: "returns every created item",
			seed: []string{
				`{"name":"first","description":"one","quantity":1}`,
				`{"name":"second","quantity":2}`,
			},
			want: []Item{
				{Name: "first", Description: "one", Quantity: 1},
				{Name: "second", Description: "", Quantity: 2},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			for _, body := range tc.seed {
				if rec := doRequest(t, router, http.MethodPost, "/items", body); rec.Code != http.StatusCreated {
					t.Fatalf("seeding item: status = %d, body = %s", rec.Code, rec.Body.String())
				}
			}

			rec := doRequest(t, router, http.MethodGet, "/items", "")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
			}
			if tc.wantBody != "" {
				if got := rec.Body.String(); got != tc.wantBody {
					t.Fatalf("body = %s, want %s", got, tc.wantBody)
				}
				return
			}

			items := decodeItems(t, rec)
			if len(items) != len(tc.want) {
				t.Fatalf("got %d items, want %d (body = %s)", len(items), len(tc.want), rec.Body.String())
			}
			byName := make(map[string]Item, len(items))
			for _, item := range items {
				byName[item.Name] = item
			}
			for _, want := range tc.want {
				got, ok := byName[want.Name]
				if !ok {
					t.Fatalf("items = %s, want one named %q", rec.Body.String(), want.Name)
				}
				requireItem(t, got, want)
			}
		})
	}
}

func TestGetItem(t *testing.T) {
	tests := []struct {
		name       string
		path       func(id uint) string
		wantStatus int
		wantItem   Item
	}{
		{
			name:       "existing item",
			path:       itemPath,
			wantStatus: http.StatusOK,
			wantItem:   Item{Name: "widget", Description: "a thing", Quantity: 4},
		},
		{
			name:       "missing item",
			path:       fixedPath("/items/999"),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "non-numeric id",
			path:       fixedPath("/items/abc"),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := mustCreateItem(t, router, "widget", "a thing", 4)

			rec := doRequest(t, router, http.MethodGet, tc.path(seeded.ID), "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				return
			}
			requireItem(t, decodeItem(t, rec), Item{
				ID:          seeded.ID,
				Name:        tc.wantItem.Name,
				Description: tc.wantItem.Description,
				Quantity:    tc.wantItem.Quantity,
			})
		})
	}
}

func TestUpdateItem(t *testing.T) {
	// wantItem is the state the item must hold afterwards, read back with a follow-up GET. It is
	// nil when the item is not expected to exist.
	tests := []struct {
		name       string
		path       func(id uint) string
		body       string
		wantStatus int
		wantItem   *Item
	}{
		{
			name:       "full replacement clears an omitted description",
			path:       itemPath,
			body:       `{"name":"new","quantity":7}`,
			wantStatus: http.StatusOK,
			wantItem:   &Item{Name: "new", Description: "", Quantity: 7},
		},
		{
			name:       "quantity zero is persisted",
			path:       itemPath,
			body:       `{"name":"zeroed","description":"now empty","quantity":0}`,
			wantStatus: http.StatusOK,
			wantItem:   &Item{Name: "zeroed", Description: "now empty", Quantity: 0},
		},
		{
			name:       "blank name leaves the item unchanged",
			path:       itemPath,
			body:       `{"name":"  ","quantity":9}`,
			wantStatus: http.StatusBadRequest,
			wantItem:   &Item{Name: "widget", Description: "a thing", Quantity: 4},
		},
		{
			name:       "absent quantity leaves the item unchanged",
			path:       itemPath,
			body:       `{"name":"renamed"}`,
			wantStatus: http.StatusBadRequest,
			wantItem:   &Item{Name: "widget", Description: "a thing", Quantity: 4},
		},
		{
			name:       "negative quantity leaves the item unchanged",
			path:       itemPath,
			body:       `{"name":"renamed","quantity":-2}`,
			wantStatus: http.StatusBadRequest,
			wantItem:   &Item{Name: "widget", Description: "a thing", Quantity: 4},
		},
		{
			name:       "missing item wins over an invalid body",
			path:       fixedPath("/items/999"),
			body:       `{}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "non-numeric id",
			path:       fixedPath("/items/abc"),
			body:       `{"name":"new","quantity":1}`,
			wantStatus: http.StatusBadRequest,
			wantItem:   &Item{Name: "widget", Description: "a thing", Quantity: 4},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := mustCreateItem(t, router, "widget", "a thing", 4)

			rec := doRequest(t, router, http.MethodPut, tc.path(seeded.ID), tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
			}

			if tc.wantItem == nil {
				return
			}
			if tc.wantStatus == http.StatusOK {
				requireItem(t, decodeItem(t, rec), Item{
					ID:          seeded.ID,
					Name:        tc.wantItem.Name,
					Description: tc.wantItem.Description,
					Quantity:    tc.wantItem.Quantity,
				})
			}

			// The stored row, not just the response, must be what the case expects.
			got := doRequest(t, router, http.MethodGet, itemPath(seeded.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading item back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireItem(t, decodeItem(t, got), Item{
				ID:          seeded.ID,
				Name:        tc.wantItem.Name,
				Description: tc.wantItem.Description,
				Quantity:    tc.wantItem.Quantity,
			})
		})
	}
}

func TestDeleteItem(t *testing.T) {
	// seed is how many items exist before the delete and path targets the one to delete; a nil path
	// means the first seeded item. wantGone asserts the targeted item answers 404 afterwards, and
	// wantRemaining is the expected row count of GET /items afterwards, with -1 skipping the check.
	tests := []struct {
		name          string
		seed          int
		path          func(ids []uint) string
		wantStatus    int
		wantGone      bool
		wantRemaining int
	}{
		{
			name:          "deletes an existing item",
			seed:          1,
			wantStatus:    http.StatusNoContent,
			wantGone:      true,
			wantRemaining: 0,
		},
		{
			name:          "missing item",
			seed:          1,
			path:          func([]uint) string { return "/items/999" },
			wantStatus:    http.StatusNotFound,
			wantRemaining: 1,
		},
		{
			name:          "non-numeric id",
			seed:          1,
			path:          func([]uint) string { return "/items/abc" },
			wantStatus:    http.StatusBadRequest,
			wantRemaining: 1,
		},
		{
			name:          "only removes the requested item",
			seed:          2,
			wantStatus:    http.StatusNoContent,
			wantGone:      true,
			wantRemaining: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			ids := make([]uint, 0, tc.seed)
			for i := 0; i < tc.seed; i++ {
				seeded := mustCreateItem(t, router, "item", "seeded", i)
				ids = append(ids, seeded.ID)
			}

			target := itemPath(ids[0])
			if tc.path != nil {
				target = tc.path(ids)
			}

			rec := doRequest(t, router, http.MethodDelete, target, "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusNoContent {
				if rec.Body.Len() != 0 {
					t.Errorf("body = %q, want an empty body", rec.Body.String())
				}
			} else {
				requireError(t, rec)
			}

			if tc.wantGone {
				gone := doRequest(t, router, http.MethodGet, itemPath(ids[0]), "")
				if gone.Code != http.StatusNotFound {
					t.Fatalf("deleted item: status = %d, want %d (body = %s)", gone.Code, http.StatusNotFound, gone.Body.String())
				}
			}

			if tc.wantRemaining < 0 {
				return
			}
			list := doRequest(t, router, http.MethodGet, "/items", "")
			if list.Code != http.StatusOK {
				t.Fatalf("listing items: status = %d, body = %s", list.Code, list.Body.String())
			}
			if items := decodeItems(t, list); len(items) != tc.wantRemaining {
				t.Errorf("remaining items = %d, want %d (body = %s)", len(items), tc.wantRemaining, list.Body.String())
			}
		})
	}
}
