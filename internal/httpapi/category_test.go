package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

// categoryPath builds the path of one category, the way a client would ask for it.
func categoryPath(id uint) string {
	return "/categories/" + strconv.FormatUint(uint64(id), 10)
}

// decodeCategory decodes a response body into a Category, so a changed JSON shape fails the test.
func decodeCategory(t *testing.T, rec *httptest.ResponseRecorder) Category {
	t.Helper()
	var category Category
	if err := json.Unmarshal(rec.Body.Bytes(), &category); err != nil {
		t.Fatalf("decoding category from %q: %v", rec.Body.String(), err)
	}
	return category
}

// decodeCategories decodes a response body into a slice of Categories.
func decodeCategories(t *testing.T, rec *httptest.ResponseRecorder) []Category {
	t.Helper()
	var categories []Category
	if err := json.Unmarshal(rec.Body.Bytes(), &categories); err != nil {
		t.Fatalf("decoding categories from %q: %v", rec.Body.String(), err)
	}
	return categories
}

// requireCategory compares the fields a response is expected to echo back.
func requireCategory(t *testing.T, got, want Category) {
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
}

// requireCategoryIDs asserts got carries the same ids, in the same order, as want.
func requireCategoryIDs(t *testing.T, got, want []Category) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d categories, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("row %d has id %d, want %d", i, got[i].ID, want[i].ID)
		}
	}
}

// requireFieldNames asserts the response body is a JSON object carrying exactly want as its keys,
// so a missing or leaked field fails the test.
func requireFieldNames(t *testing.T, rec *httptest.ResponseRecorder, want ...string) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
	}
	if len(body) != len(want) {
		t.Fatalf("body = %s, want exactly the keys %v", rec.Body.String(), want)
	}
	for _, key := range want {
		if _, ok := body[key]; !ok {
			t.Errorf("body = %s, want a %q key", rec.Body.String(), key)
		}
	}
}

// listCategoryNames reads GET /categories and returns its rows as id -> name.
func listCategoryNames(t *testing.T, router *gin.Engine) map[uint]string {
	t.Helper()
	rec := doRequest(t, router, http.MethodGet, "/categories", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("listing categories: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	categories := decodeCategories(t, rec)
	names := make(map[uint]string, len(categories))
	for _, category := range categories {
		names[category.ID] = category.Name
	}
	return names
}

// mustCreateCategory creates a category through the API, so a test can start from a known row.
func mustCreateCategory(t *testing.T, router *gin.Engine, name string) Category {
	t.Helper()
	payload, err := json.Marshal(categoryRequest{Name: name})
	if err != nil {
		t.Fatalf("encoding create body: %v", err)
	}
	rec := doRequest(t, router, http.MethodPost, "/categories", string(payload))
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating category: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return decodeCategory(t, rec)
}

// seedCategories creates count categories through the API and returns them in creation order, so
// their ids ascend. The names ascend with the index, which makes them sortable.
func seedCategories(t *testing.T, router *gin.Engine, count int) []Category {
	t.Helper()
	categories := make([]Category, 0, count)
	for i := 0; i < count; i++ {
		categories = append(categories, mustCreateCategory(t, router, fmt.Sprintf("cat-%02d", i)))
	}
	return categories
}

func TestCreateCategory(t *testing.T) {
	// seed is the names that already exist before the request; created says whether the request is
	// expected to add a row, and wantName is the name it must store.
	tests := []struct {
		name       string
		seed       []string
		body       string
		wantStatus int
		created    bool
		wantName   string
	}{
		{
			name:       "valid name",
			body:       `{"name":"Work"}`,
			wantStatus: http.StatusCreated,
			created:    true,
			wantName:   "Work",
		},
		{
			name:       "omitted name",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty name",
			body:       `{"name":""}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "blank name",
			body:       `{"name":"   "}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			body:       `{"name":`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "duplicate name",
			seed:       []string{"Work"},
			body:       `{"name":"Work"}`,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "name differing only by surrounding whitespace is stored",
			seed:       []string{"Work"},
			body:       `{"name":"Work "}`,
			wantStatus: http.StatusCreated,
			created:    true,
			wantName:   "Work ",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			before := make(map[uint]string, len(tc.seed))
			for _, name := range tc.seed {
				seeded := mustCreateCategory(t, router, name)
				before[seeded.ID] = seeded.Name
			}

			rec := doRequest(t, router, http.MethodPost, "/categories", tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			names := listCategoryNames(t, router)
			if !tc.created {
				requireError(t, rec)

				// A rejected create must not have changed, removed or added a row.
				if len(names) != len(before) {
					t.Fatalf("categories after a rejected create = %v, want %v", names, before)
				}
				for id, name := range before {
					if names[id] != name {
						t.Errorf("category %d name = %q after a rejected create, want %q", id, names[id], name)
					}
				}
				return
			}

			created := decodeCategory(t, rec)
			requireFieldNames(t, rec, "id", "name")
			requireCategory(t, created, Category{Name: tc.wantName})

			if len(names) != len(before)+1 {
				t.Fatalf("categories = %v, want %d rows including the created one", names, len(before)+1)
			}
			if names[created.ID] != tc.wantName {
				t.Errorf("stored name = %q, want %q", names[created.ID], tc.wantName)
			}

			// The stored row, not just the response, must carry the name as sent.
			got := doRequest(t, router, http.MethodGet, categoryPath(created.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading category back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireCategory(t, decodeCategory(t, got), Category{ID: created.ID, Name: tc.wantName})
		})
	}
}

func TestListCategories(t *testing.T) {
	const seededCount = 5

	tests := []struct {
		name      string
		seed      bool
		query     string
		wantTotal string
		wantBody  string
		check     func(t *testing.T, seeded, got []Category)
	}{
		{
			name:      "empty database is an empty array",
			wantTotal: "0",
			wantBody:  "[]",
		},
		{
			name:      "defaults return every category in id order",
			seed:      true,
			wantTotal: "5",
			check: func(t *testing.T, seeded, got []Category) {
				t.Helper()
				requireCategoryIDs(t, got, seeded)
			},
		},
		{
			name:      "sort by name ascending keeps id order",
			seed:      true,
			query:     "?sort=name&order=asc",
			wantTotal: "5",
			check: func(t *testing.T, seeded, got []Category) {
				t.Helper()
				requireCategoryIDs(t, got, seeded)
				for i := 1; i < len(got); i++ {
					if got[i].Name < got[i-1].Name {
						t.Fatalf("row %d has name %q, below the previous %q; want ascending names", i, got[i].Name, got[i-1].Name)
					}
				}
			},
		},
		{
			name:      "sort by name descending reverses the names",
			seed:      true,
			query:     "?sort=name&order=desc",
			wantTotal: "5",
			check: func(t *testing.T, seeded, got []Category) {
				t.Helper()
				want := make([]Category, 0, len(seeded))
				for i := len(seeded) - 1; i >= 0; i-- {
					want = append(want, seeded[i])
				}
				requireCategoryIDs(t, got, want)
				for i := 1; i < len(got); i++ {
					if got[i].Name > got[i-1].Name {
						t.Fatalf("row %d has name %q, above the previous %q; want descending names", i, got[i].Name, got[i-1].Name)
					}
				}
			},
		},
		{
			name:      "limit and offset select a middle page",
			seed:      true,
			query:     "?limit=2&offset=1",
			wantTotal: "5",
			check: func(t *testing.T, seeded, got []Category) {
				t.Helper()
				requireCategoryIDs(t, got, seeded[1:3])
			},
		},
		{
			name:      "offset past the last row is an empty array",
			seed:      true,
			query:     "?offset=100",
			wantTotal: "5",
			wantBody:  "[]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := make([]Category, 0, seededCount)
			if tc.seed {
				seeded = seedCategories(t, router, seededCount)
			}

			rec := doRequest(t, router, http.MethodGet, "/categories"+tc.query, "")

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
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

			got := decodeCategories(t, rec)
			if tc.check != nil {
				tc.check(t, seeded, got)
			}
		})
	}
}

func TestListCategoriesInvalidParameters(t *testing.T) {
	// The exact wording every invalid parameter produces. The sort message lists this resource's
	// sortable keys in their fixed order, which differ from the items and tasks ones.
	const (
		wantLimit  = "limit must be an integer from 1 to 100"
		wantOffset = "offset must be zero or greater"
		wantSort   = "sort must be one of id, name"
		wantOrder  = "order must be asc or desc"
	)

	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{
			name:    "limit zero",
			query:   "?limit=0",
			wantMsg: wantLimit,
		},
		{
			name:    "limit above the maximum",
			query:   "?limit=101",
			wantMsg: wantLimit,
		},
		{
			name:    "limit not a number",
			query:   "?limit=abc",
			wantMsg: wantLimit,
		},
		{
			name:    "limit present but empty",
			query:   "?limit=",
			wantMsg: wantLimit,
		},
		{
			name:    "offset negative",
			query:   "?offset=-1",
			wantMsg: wantOffset,
		},
		{
			name:    "offset not a number",
			query:   "?offset=abc",
			wantMsg: wantOffset,
		},
		{
			name:    "offset present but empty",
			query:   "?offset=",
			wantMsg: wantOffset,
		},
		{
			name:    "item quantity is not a category sort key",
			query:   "?sort=quantity",
			wantMsg: wantSort,
		},
		{
			name:    "task title is not a category sort key",
			query:   "?sort=title",
			wantMsg: wantSort,
		},
		{
			name:    "unknown sort",
			query:   "?sort=bogus",
			wantMsg: wantSort,
		},
		{
			name:    "sort present but empty",
			query:   "?sort=",
			wantMsg: wantSort,
		},
		{
			name:    "order uppercase",
			query:   "?order=ASC",
			wantMsg: wantOrder,
		},
		{
			name:    "unknown order",
			query:   "?order=sideways",
			wantMsg: wantOrder,
		},
		{
			name:    "order present but empty",
			query:   "?order=",
			wantMsg: wantOrder,
		},
		{
			name:    "limit is checked before sort",
			query:   "?limit=0&sort=bogus",
			wantMsg: wantLimit,
		},
		{
			name:    "offset is checked before order",
			query:   "?offset=-1&order=sideways",
			wantMsg: wantOffset,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seedCategories(t, router, 3)

			rec := doRequest(t, router, http.MethodGet, "/categories"+tc.query, "")

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			requireError(t, rec)
			if got := errorMessage(t, rec); got != tc.wantMsg {
				t.Errorf("error message = %q, want %q", got, tc.wantMsg)
			}
			if got := rec.Header().Get(TotalCountHeader); got != "" {
				t.Errorf("%s = %q on a rejected request, want no header", TotalCountHeader, got)
			}

			// The rejected request must not have touched the table: the seeded rows are still
			// there and still listable.
			names := listCategoryNames(t, router)
			if len(names) != 3 {
				t.Errorf("categories after a rejected request = %v, want the 3 seeded rows", names)
			}
		})
	}
}

func TestGetCategory(t *testing.T) {
	tests := []struct {
		name       string
		path       func(id uint) string
		wantStatus int
		wantName   string
	}{
		{
			name:       "existing category",
			path:       categoryPath,
			wantStatus: http.StatusOK,
			wantName:   "widget",
		},
		{
			name:       "missing category",
			path:       fixedPath("/categories/999"),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "non-numeric id",
			path:       fixedPath("/categories/abc"),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := mustCreateCategory(t, router, "widget")

			rec := doRequest(t, router, http.MethodGet, tc.path(seeded.ID), "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				return
			}
			requireFieldNames(t, rec, "id", "name")
			requireCategory(t, decodeCategory(t, rec), Category{ID: seeded.ID, Name: tc.wantName})
		})
	}
}

func TestUpdateCategory(t *testing.T) {
	// The target category starts out as "alpha" and an unrelated one as "beta". wantName is the name
	// the target must store afterwards; it is empty when the target is not expected to be found.
	tests := []struct {
		name       string
		path       func(id uint) string
		body       string
		wantStatus int
		wantName   string
	}{
		{
			name:       "renames the category",
			body:       `{"name":"Renamed"}`,
			wantStatus: http.StatusOK,
			wantName:   "Renamed",
		},
		{
			name:       "rename to its own current name",
			body:       `{"name":"alpha"}`,
			wantStatus: http.StatusOK,
			wantName:   "alpha",
		},
		{
			name:       "name of another category",
			body:       `{"name":"beta"}`,
			wantStatus: http.StatusConflict,
			wantName:   "alpha",
		},
		{
			name:       "missing name",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantName:   "alpha",
		},
		{
			name:       "empty name",
			body:       `{"name":""}`,
			wantStatus: http.StatusBadRequest,
			wantName:   "alpha",
		},
		{
			name:       "blank name",
			body:       `{"name":"  "}`,
			wantStatus: http.StatusBadRequest,
			wantName:   "alpha",
		},
		{
			name:       "malformed json",
			body:       `{"name":`,
			wantStatus: http.StatusBadRequest,
			wantName:   "alpha",
		},
		{
			name:       "missing category wins over an invalid body",
			path:       fixedPath("/categories/999"),
			body:       `{}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "non-numeric id",
			path:       fixedPath("/categories/abc"),
			body:       `{"name":"Renamed"}`,
			wantStatus: http.StatusBadRequest,
			wantName:   "alpha",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			target := mustCreateCategory(t, router, "alpha")
			other := mustCreateCategory(t, router, "beta")

			path := categoryPath(target.ID)
			if tc.path != nil {
				path = tc.path(target.ID)
			}

			rec := doRequest(t, router, http.MethodPut, path, tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantStatus == http.StatusOK {
				requireFieldNames(t, rec, "id", "name")
				requireCategory(t, decodeCategory(t, rec), Category{ID: target.ID, Name: tc.wantName})
			} else {
				requireError(t, rec)
			}

			// The rename must not disturb any other row: the whole table holds exactly the two
			// seeded categories, the unrelated one is untouched and the target holds wantName.
			names := listCategoryNames(t, router)
			if len(names) != 2 {
				t.Fatalf("categories = %v, want exactly the two seeded rows", names)
			}
			if got := names[other.ID]; got != "beta" {
				t.Errorf("unrelated category name = %q, want %q", got, "beta")
			}
			if tc.wantName != "" {
				if got := names[target.ID]; got != tc.wantName {
					t.Errorf("category %d name = %q, want %q", target.ID, got, tc.wantName)
				}
			}

			if tc.wantStatus != http.StatusOK {
				return
			}

			// The stored row, not just the response, must carry the new name.
			got := doRequest(t, router, http.MethodGet, categoryPath(target.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading category back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireCategory(t, decodeCategory(t, got), Category{ID: target.ID, Name: tc.wantName})
		})
	}

	t.Run("renaming is possible again once the conflicting name is free", func(t *testing.T) {
		router := newTestRouter(t)
		alpha := mustCreateCategory(t, router, "alpha")
		beta := mustCreateCategory(t, router, "beta")

		conflict := doRequest(t, router, http.MethodPut, categoryPath(beta.ID), `{"name":"alpha"}`)
		if conflict.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body = %s)", conflict.Code, http.StatusConflict, conflict.Body.String())
		}

		renamed := doRequest(t, router, http.MethodPut, categoryPath(alpha.ID), `{"name":"gamma"}`)
		if renamed.Code != http.StatusOK {
			t.Fatalf("renaming the conflicting category: status = %d, body = %s", renamed.Code, renamed.Body.String())
		}

		rec := doRequest(t, router, http.MethodPut, categoryPath(beta.ID), `{"name":"alpha"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		requireCategory(t, decodeCategory(t, rec), Category{ID: beta.ID, Name: "alpha"})
	})
}

func TestDeleteCategory(t *testing.T) {
	// referenced seeds an item pointing at the target category, and wantRemaining is the number of
	// categories left afterwards.
	tests := []struct {
		name          string
		path          func(id uint) string
		referenced    bool
		wantStatus    int
		wantRemaining int
	}{
		{
			name:          "deletes an unreferenced category",
			wantStatus:    http.StatusNoContent,
			wantRemaining: 1,
		},
		{
			name:          "refuses while an item references the category",
			referenced:    true,
			wantStatus:    http.StatusConflict,
			wantRemaining: 2,
		},
		{
			name:          "missing category",
			path:          fixedPath("/categories/999"),
			wantStatus:    http.StatusNotFound,
			wantRemaining: 2,
		},
		{
			name:          "non-numeric id",
			path:          fixedPath("/categories/abc"),
			wantStatus:    http.StatusBadRequest,
			wantRemaining: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			target := mustCreateCategory(t, router, "target")
			mustCreateCategory(t, router, "other")
			if tc.referenced {
				mustCreateItemInCategory(t, router, "referencing", "seeded", 1, &target.ID)
			}

			path := categoryPath(target.ID)
			if tc.path != nil {
				path = tc.path(target.ID)
			}

			rec := doRequest(t, router, http.MethodDelete, path, "")

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
			if tc.wantStatus == http.StatusConflict {
				if got, want := rec.Body.String(), `{"error":"category has items"}`; got != want {
					t.Errorf("body = %s, want %s", got, want)
				}
			}

			// A deleted category must be gone; a rejected delete must have left it alone.
			got := doRequest(t, router, http.MethodGet, categoryPath(target.ID), "")
			if tc.wantStatus == http.StatusNoContent {
				if got.Code != http.StatusNotFound {
					t.Fatalf("deleted category: status = %d, want %d (body = %s)", got.Code, http.StatusNotFound, got.Body.String())
				}
			} else {
				if got.Code != http.StatusOK {
					t.Fatalf("category after a rejected delete: status = %d, want %d (body = %s)", got.Code, http.StatusOK, got.Body.String())
				}
				requireCategory(t, decodeCategory(t, got), Category{ID: target.ID, Name: "target"})
			}

			names := listCategoryNames(t, router)
			if len(names) != tc.wantRemaining {
				t.Errorf("remaining categories = %d, want %d (names = %v)", len(names), tc.wantRemaining, names)
			}
		})
	}

	t.Run("becomes possible once the last referencing item is gone", func(t *testing.T) {
		router := newTestRouter(t)
		category := mustCreateCategory(t, router, "alpha")
		item := mustCreateItemInCategory(t, router, "referencing", "seeded", 1, &category.ID)

		conflict := doRequest(t, router, http.MethodDelete, categoryPath(category.ID), "")
		if conflict.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d (body = %s)", conflict.Code, http.StatusConflict, conflict.Body.String())
		}

		if rec := doRequest(t, router, http.MethodDelete, itemPath(item.ID), ""); rec.Code != http.StatusNoContent {
			t.Fatalf("deleting the item: status = %d, body = %s", rec.Code, rec.Body.String())
		}

		rec := doRequest(t, router, http.MethodDelete, categoryPath(category.ID), "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d (body = %s)", rec.Code, http.StatusNoContent, rec.Body.String())
		}

		gone := doRequest(t, router, http.MethodGet, categoryPath(category.ID), "")
		if gone.Code != http.StatusNotFound {
			t.Fatalf("deleted category: status = %d, want %d (body = %s)", gone.Code, http.StatusNotFound, gone.Body.String())
		}
	})
}
