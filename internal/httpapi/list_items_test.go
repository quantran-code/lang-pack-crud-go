package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// requireItemIDs asserts got carries the same ids, in the same order, as want.
func requireItemIDs(t *testing.T, got, want []Item) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("row %d has id %d, want %d", i, got[i].ID, want[i].ID)
		}
	}
}

// seedItems creates count items through the API and returns them in creation order, so their ids
// ascend. The name and quantity follow the index, which makes each of them sortable, except that
// the last two items share one name and one quantity so the id tie-break is observable.
func seedItems(t *testing.T, router *gin.Engine, count int) []Item {
	t.Helper()
	items := make([]Item, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("item-%02d", i)
		quantity := i + 1
		if count >= 2 && i >= count-2 {
			name = "item-tie"
			quantity = count * 10
		}
		items = append(items, mustCreateItem(t, router, name, "seeded", quantity))
	}
	return items
}

func TestListItemsPagination(t *testing.T) {
	const seededCount = 25

	tests := []struct {
		name      string
		query     string
		wantTotal string
		wantBody  string
		check     func(t *testing.T, seeded, got []Item)
	}{
		{
			name:      "defaults return the first page in id order",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Item) {
				t.Helper()
				requireItemIDs(t, got, seeded[:defaultLimit])
			},
		},
		{
			name:      "limit alone takes the first page",
			query:     "?limit=10",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Item) {
				t.Helper()
				requireItemIDs(t, got, seeded[:10])
			},
		},
		{
			name:      "limit and offset select a middle page",
			query:     "?limit=10&offset=10",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Item) {
				t.Helper()
				requireItemIDs(t, got, seeded[10:20])
			},
		},
		{
			name:      "limit at the maximum returns every row",
			query:     "?limit=100",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Item) {
				t.Helper()
				requireItemIDs(t, got, seeded)
			},
		},
		{
			name:      "sort by name descending",
			query:     "?sort=name&order=desc",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Item) {
				t.Helper()
				if len(got) != defaultLimit {
					t.Fatalf("got %d items, want the default page of %d", len(got), defaultLimit)
				}
				for i := 1; i < len(got); i++ {
					if got[i].Name > got[i-1].Name {
						t.Fatalf("row %d has name %q, above the previous %q; want descending names", i, got[i].Name, got[i-1].Name)
					}
				}
				// The two items sharing the highest name come first, ordered by id ascending.
				if got[0].Name != "item-tie" || got[1].Name != "item-tie" {
					t.Fatalf("first two names = %q, %q, want both %q", got[0].Name, got[1].Name, "item-tie")
				}
				if got[0].ID >= got[1].ID {
					t.Errorf("ids of equal names = %d, %d, want ascending", got[0].ID, got[1].ID)
				}
			},
		},
		{
			name:      "sort by quantity descending",
			query:     "?sort=quantity&order=desc",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Item) {
				t.Helper()
				if len(got) != defaultLimit {
					t.Fatalf("got %d items, want the default page of %d", len(got), defaultLimit)
				}
				for i := 1; i < len(got); i++ {
					if got[i].Quantity > got[i-1].Quantity {
						t.Fatalf("row %d has quantity %d, above the previous %d; want descending quantities", i, got[i].Quantity, got[i-1].Quantity)
					}
				}
				want := seededCount * 10
				if got[0].Quantity != want || got[1].Quantity != want {
					t.Fatalf("first two quantities = %d, %d, want both %d", got[0].Quantity, got[1].Quantity, want)
				}
				if got[0].ID >= got[1].ID {
					t.Errorf("ids of equal quantities = %d, %d, want ascending", got[0].ID, got[1].ID)
				}
			},
		},
		{
			name:      "rows with equal sort values are ordered by id",
			query:     "?sort=name&order=asc&limit=100",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Item) {
				t.Helper()
				requireItemIDs(t, got, seeded)
				if got[len(got)-2].Name != "item-tie" || got[len(got)-1].Name != "item-tie" {
					t.Fatalf("last two names = %q, %q, want both %q", got[len(got)-2].Name, got[len(got)-1].Name, "item-tie")
				}
				if got[len(got)-2].ID >= got[len(got)-1].ID {
					t.Errorf("ids of equal names = %d, %d, want ascending", got[len(got)-2].ID, got[len(got)-1].ID)
				}
			},
		},
		{
			name:      "offset past the end is an empty array",
			query:     "?offset=1000",
			wantTotal: "25",
			wantBody:  "[]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := seedItems(t, router, seededCount)

			rec := doRequest(t, router, http.MethodGet, "/items"+tc.query, "")

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

			got := decodeItems(t, rec)
			if tc.check != nil {
				tc.check(t, seeded, got)
			}
		})
	}
}

func TestListItemsInvalidParameters(t *testing.T) {
	// The exact wording every invalid parameter produces. The sort message lists this resource's
	// sortable keys in their fixed order.
	const (
		wantLimit  = "limit must be an integer from 1 to 100"
		wantOffset = "offset must be zero or greater"
		wantSort   = "sort must be one of id, name, quantity"
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
			name:    "sort that looks like SQL",
			query:   "?sort=name%3BDROP%20TABLE%20items",
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
			seedItems(t, router, 3)

			rec := doRequest(t, router, http.MethodGet, "/items"+tc.query, "")

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
			list := doRequest(t, router, http.MethodGet, "/items", "")
			if list.Code != http.StatusOK {
				t.Fatalf("listing items after a rejected request: status = %d, body = %s", list.Code, list.Body.String())
			}
			if got := list.Header().Get(TotalCountHeader); got != "3" {
				t.Errorf("%s after a rejected request = %q, want %q", TotalCountHeader, got, "3")
			}
		})
	}
}
