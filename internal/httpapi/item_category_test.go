package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// mustCreateItemInCategory creates an item through the API, optionally pointing at a category, so a
// test can start from a known row. A nil categoryID creates an item with no category.
func mustCreateItemInCategory(t *testing.T, router *gin.Engine, name, description string, quantity int, categoryID *uint) Item {
	t.Helper()
	payload, err := json.Marshal(itemRequest{
		Name:        name,
		Description: description,
		Quantity:    &quantity,
		CategoryID:  categoryID,
	})
	if err != nil {
		t.Fatalf("encoding create body: %v", err)
	}
	rec := doRequest(t, router, http.MethodPost, "/items", string(payload))
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating item: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return decodeItem(t, rec)
}

// requireCategoryID asserts got carries want: both null, or both pointing at the same id.
func requireCategoryID(t *testing.T, got, want *uint) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("category_id = %d, want null", *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("category_id = null, want %d", *want)
	}
	if *got != *want {
		t.Errorf("category_id = %d, want %d", *got, *want)
	}
}

// idsOf returns the ids of items in the order they appear.
func idsOf(items []Item) []uint {
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// requireItemIDsInOrder asserts got carries exactly want as ids, in the same order.
func requireItemIDsInOrder(t *testing.T, got []Item, want []uint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d (ids = %v, want %v)", len(got), len(want), idsOf(got), want)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("row %d has id %d, want %d (ids = %v)", i, got[i].ID, want[i], idsOf(got))
		}
	}
}

func TestCreateItemCategory(t *testing.T) {
	// body builds the request body from the id of the category seeded for the case, so the same
	// request can reference a stored category or one that does not exist. wantCat is the
	// category_id the item must hold afterwards, wantRaw the exact body a rejected request must
	// produce, and wantItems the row count of GET /items afterwards.
	tests := []struct {
		name       string
		body       func(categoryID uint) string
		wantStatus int
		wantCat    func(categoryID uint) *uint
		wantRaw    string
		wantItems  int
	}{
		{
			name: "valid category_id",
			body: func(id uint) string {
				return fmt.Sprintf(`{"name":"widget","description":"a thing","quantity":2,"category_id":%d}`, id)
			},
			wantStatus: http.StatusCreated,
			wantCat:    func(id uint) *uint { return &id },
			wantItems:  1,
		},
		{
			name:       "unknown category_id",
			body:       func(uint) string { return `{"name":"widget","description":"a thing","quantity":2,"category_id":999}` },
			wantStatus: http.StatusBadRequest,
			wantRaw:    `{"error":"category does not exist"}`,
			wantItems:  0,
		},
		{
			name:       "omitted category_id",
			body:       func(uint) string { return `{"name":"widget","description":"a thing","quantity":2}` },
			wantStatus: http.StatusCreated,
			wantCat:    func(uint) *uint { return nil },
			wantItems:  1,
		},
		{
			name:       "null category_id",
			body:       func(uint) string { return `{"name":"widget","description":"a thing","quantity":2,"category_id":null}` },
			wantStatus: http.StatusCreated,
			wantCat:    func(uint) *uint { return nil },
			wantItems:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			// Every case runs against a stored category, so an omitted or null reference is a real
			// choice and the unknown id really is unknown.
			category := mustCreateCategory(t, router, "seeded")

			rec := doRequest(t, router, http.MethodPost, "/items", tc.body(category.ID))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusCreated {
				requireError(t, rec)
				if tc.wantRaw != "" {
					if got := rec.Body.String(); got != tc.wantRaw {
						t.Errorf("body = %s, want %s", got, tc.wantRaw)
					}
				}
			}

			// A rejected create must not have added a row.
			list := doRequest(t, router, http.MethodGet, "/items", "")
			if list.Code != http.StatusOK {
				t.Fatalf("listing items: status = %d, body = %s", list.Code, list.Body.String())
			}
			if items := decodeItems(t, list); len(items) != tc.wantItems {
				t.Fatalf("items = %d, want %d (body = %s)", len(items), tc.wantItems, list.Body.String())
			}

			if tc.wantStatus != http.StatusCreated {
				return
			}

			want := tc.wantCat(category.ID)
			created := decodeItem(t, rec)
			requireItem(t, created, Item{Name: "widget", Description: "a thing", Quantity: 2})
			requireCategoryID(t, created.CategoryID, want)

			// The stored row, not just the response, must carry the reference.
			got := doRequest(t, router, http.MethodGet, itemPath(created.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading item back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireCategoryID(t, decodeItem(t, got).CategoryID, want)
		})
	}
}

func TestUpdateItemCategory(t *testing.T) {
	// Every case starts from an item named "widget", described as "a thing", with quantity 4, in the
	// first category. wantItem is the state the item must hold afterwards, read back with a
	// follow-up GET, so a rejected request is checked against the untouched row. wantRaw, when set,
	// is the exact body the rejected request must produce.
	tests := []struct {
		name       string
		body       func(catA, catB uint) string
		wantStatus int
		wantItem   func(catA, catB uint) Item
		wantRaw    string
	}{
		{
			name: "sets a different category",
			body: func(_, catB uint) string {
				return fmt.Sprintf(`{"name":"renamed","description":"updated","quantity":9,"category_id":%d}`, catB)
			},
			wantStatus: http.StatusOK,
			wantItem: func(_, catB uint) Item {
				return Item{Name: "renamed", Description: "updated", Quantity: 9, CategoryID: &catB}
			},
		},
		{
			name: "keeps the same category",
			body: func(catA, _ uint) string {
				return fmt.Sprintf(`{"name":"widget","description":"a thing","quantity":4,"category_id":%d}`, catA)
			},
			wantStatus: http.StatusOK,
			wantItem: func(catA, _ uint) Item {
				return Item{Name: "widget", Description: "a thing", Quantity: 4, CategoryID: &catA}
			},
		},
		{
			name:       "clears the category by omitting it",
			body:       func(_, _ uint) string { return `{"name":"cleared","description":"kept","quantity":3}` },
			wantStatus: http.StatusOK,
			wantItem:   func(_, _ uint) Item { return Item{Name: "cleared", Description: "kept", Quantity: 3} },
		},
		{
			name: "clears the category with an explicit null",
			body: func(_, _ uint) string {
				return `{"name":"cleared","description":"kept","quantity":3,"category_id":null}`
			},
			wantStatus: http.StatusOK,
			wantItem:   func(_, _ uint) Item { return Item{Name: "cleared", Description: "kept", Quantity: 3} },
		},
		{
			name: "unknown category_id leaves the item unchanged",
			body: func(_, _ uint) string {
				return `{"name":"renamed","description":"updated","quantity":9,"category_id":999}`
			},
			wantStatus: http.StatusBadRequest,
			wantItem: func(catA, _ uint) Item {
				return Item{Name: "widget", Description: "a thing", Quantity: 4, CategoryID: &catA}
			},
			wantRaw: `{"error":"category does not exist"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			catA := mustCreateCategory(t, router, "alpha")
			catB := mustCreateCategory(t, router, "beta")
			seeded := mustCreateItemInCategory(t, router, "widget", "a thing", 4, &catA.ID)

			rec := doRequest(t, router, http.MethodPut, itemPath(seeded.ID), tc.body(catA.ID, catB.ID))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			want := tc.wantItem(catA.ID, catB.ID)
			want.ID = seeded.ID

			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				if tc.wantRaw != "" {
					if got := rec.Body.String(); got != tc.wantRaw {
						t.Errorf("body = %s, want %s", got, tc.wantRaw)
					}
				}
			} else {
				updated := decodeItem(t, rec)
				requireItem(t, updated, want)
				requireCategoryID(t, updated.CategoryID, want.CategoryID)
			}

			// The stored row, not just the response, must be what the case expects.
			got := doRequest(t, router, http.MethodGet, itemPath(seeded.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading item back: status = %d, body = %s", got.Code, got.Body.String())
			}
			stored := decodeItem(t, got)
			requireItem(t, stored, want)
			requireCategoryID(t, stored.CategoryID, want.CategoryID)
		})
	}
}

func TestListItemCategoryFilter(t *testing.T) {
	// itemGroups is the seeded table: two categories, three items in the first, two in the second
	// and two with no category, with every item kept in creation order so ids ascend.
	type itemGroups struct {
		catA       uint
		catB       uint
		inA        []Item
		inB        []Item
		unassigned []Item
		all        []Item
	}

	seed := func(t *testing.T, router *gin.Engine) itemGroups {
		t.Helper()
		catA := mustCreateCategory(t, router, "alpha")
		catB := mustCreateCategory(t, router, "beta")
		groups := itemGroups{catA: catA.ID, catB: catB.ID}
		create := func(name, description string, quantity int, categoryID *uint) Item {
			item := mustCreateItemInCategory(t, router, name, description, quantity, categoryID)
			groups.all = append(groups.all, item)
			return item
		}

		for i := 0; i < 3; i++ {
			groups.inA = append(groups.inA, create(fmt.Sprintf("a-%02d", i), "in alpha", i+1, &catA.ID))
		}
		groups.unassigned = append(groups.unassigned, create("none-00", "no category", 10, nil))
		for i := 0; i < 2; i++ {
			groups.inB = append(groups.inB, create(fmt.Sprintf("b-%02d", i), "in beta", i+1, &catB.ID))
		}
		groups.unassigned = append(groups.unassigned, create("none-01", "no category", 11, nil))
		return groups
	}

	tests := []struct {
		name       string
		query      func(g itemGroups) string
		wantStatus int
		wantTotal  string
		wantBody   string
		check      func(t *testing.T, g itemGroups, got []Item)
	}{
		{
			name:       "filters to one category",
			query:      func(g itemGroups) string { return fmt.Sprintf("?category_id=%d", g.catA) },
			wantStatus: http.StatusOK,
			wantTotal:  "3",
			check: func(t *testing.T, g itemGroups, got []Item) {
				t.Helper()
				requireItemIDsInOrder(t, got, idsOf(g.inA))
				for _, item := range got {
					requireCategoryID(t, item.CategoryID, &g.catA)
				}
			},
		},
		{
			name:       "filters to the other category",
			query:      func(g itemGroups) string { return fmt.Sprintf("?category_id=%d", g.catB) },
			wantStatus: http.StatusOK,
			wantTotal:  "2",
			check: func(t *testing.T, g itemGroups, got []Item) {
				t.Helper()
				requireItemIDsInOrder(t, got, idsOf(g.inB))
				for _, item := range got {
					requireCategoryID(t, item.CategoryID, &g.catB)
				}
			},
		},
		{
			name:       "unknown category id matches nothing",
			query:      func(itemGroups) string { return "?category_id=999" },
			wantStatus: http.StatusOK,
			wantTotal:  "0",
			wantBody:   "[]",
		},
		{
			name:       "non-numeric category id",
			query:      func(itemGroups) string { return "?category_id=abc" },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "category id present but empty",
			query:      func(itemGroups) string { return "?category_id=" },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "pagination stays inside the filtered set",
			query:      func(g itemGroups) string { return fmt.Sprintf("?category_id=%d&limit=1&offset=1", g.catA) },
			wantStatus: http.StatusOK,
			wantTotal:  "3",
			check: func(t *testing.T, g itemGroups, got []Item) {
				t.Helper()
				requireItemIDsInOrder(t, got, idsOf(g.inA[1:2]))
				for _, item := range got {
					requireCategoryID(t, item.CategoryID, &g.catA)
				}
			},
		},
		{
			name:       "no filter returns every item",
			query:      func(itemGroups) string { return "" },
			wantStatus: http.StatusOK,
			wantTotal:  "7",
			check: func(t *testing.T, g itemGroups, got []Item) {
				t.Helper()
				requireItemIDsInOrder(t, got, idsOf(g.all))
				// The two items with no category are part of an unfiltered page.
				nulls := 0
				for _, item := range got {
					if item.CategoryID == nil {
						nulls++
					}
				}
				if nulls != len(g.unassigned) {
					t.Errorf("items with no category = %d, want %d", nulls, len(g.unassigned))
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			groups := seed(t, router)

			rec := doRequest(t, router, http.MethodGet, "/items"+tc.query(groups), "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				if got := rec.Header().Get(TotalCountHeader); got != "" {
					t.Errorf("%s = %q on a rejected request, want no header", TotalCountHeader, got)
				}

				// The rejected request must not have touched the table.
				list := doRequest(t, router, http.MethodGet, "/items", "")
				if list.Code != http.StatusOK {
					t.Fatalf("listing items after a rejected request: status = %d, body = %s", list.Code, list.Body.String())
				}
				if got := list.Header().Get(TotalCountHeader); got != "7" {
					t.Errorf("%s after a rejected request = %q, want %q", TotalCountHeader, got, "7")
				}
				return
			}

			if got := rec.Header().Get(TotalCountHeader); got != tc.wantTotal {
				t.Errorf("%s = %q, want %q", TotalCountHeader, got, tc.wantTotal)
			}
			if tc.wantBody != "" {
				if got := rec.Body.String(); got != tc.wantBody {
					t.Fatalf("body = %s, want %s", got, tc.wantBody)
				}
				return
			}
			if tc.check != nil {
				tc.check(t, groups, decodeItems(t, rec))
			}
		})
	}
}
