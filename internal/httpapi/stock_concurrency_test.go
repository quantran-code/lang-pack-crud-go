package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

// adjustStatus fires one adjustment against the router and returns only its status code. It takes
// no *testing.T and calls no testing method, so it is safe to call from a goroutine: the status is
// reported back to the test goroutine and asserted there. The request carries the shared admin
// token, which testAdminToken() also builds without a *testing.T.
func adjustStatus(router *gin.Engine, itemID uint, delta int) int {
	body := fmt.Sprintf(`{"delta":%d}`, delta)
	req := httptest.NewRequest(http.MethodPost, adjustPath(itemID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authorizationHeader(testAdminToken()))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}

func TestConcurrentAdjustments(t *testing.T) {
	// Each case seeds one item with startQuantity, fires requests adjustments at it all at once and
	// expects wantOK of them to be answered 200 and wantConflict of them 409, leaving the item on
	// wantFinalQuantity with wantMovements recorded. The increments row has enough stock for all of
	// them, so it must never lose an update; the decrements row has stock for exactly half of them,
	// so exactly that many may succeed.
	tests := []struct {
		name              string
		startQuantity     int
		delta             int
		requests          int
		wantOK            int
		wantConflict      int
		wantFinalQuantity int
		wantMovements     int
	}{
		{
			name:              "thirty concurrent increments never lose an update",
			startQuantity:     0,
			delta:             1,
			requests:          30,
			wantOK:            30,
			wantConflict:      0,
			wantFinalQuantity: 30,
			wantMovements:     30,
		},
		{
			name:              "ten concurrent decrements only spend the stock that exists",
			startQuantity:     5,
			delta:             -1,
			requests:          10,
			wantOK:            5,
			wantConflict:      5,
			wantFinalQuantity: 0,
			wantMovements:     5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			item := mustCreateItem(t, router, "widget", "a thing", tc.startQuantity)

			// Every goroutine blocks on start until all of them are launched, so the requests
			// really overlap instead of running one after another at the call site. Each status is
			// written to its own slot, so the slice is never appended to concurrently.
			statuses := make([]int, tc.requests)
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(tc.requests)
			for i := 0; i < tc.requests; i++ {
				go func(i int) {
					defer wg.Done()
					<-start
					statuses[i] = adjustStatus(router, item.ID, tc.delta)
				}(i)
			}
			close(start)
			wg.Wait()

			// Only now, back on the test goroutine, is anything asserted.
			ok, conflict := 0, 0
			for i, status := range statuses {
				if status >= http.StatusInternalServerError {
					t.Fatalf("request %d status = %d, want no 5xx: a SQLite lock error must never reach the client", i, status)
				}
				switch status {
				case http.StatusOK:
					ok++
				case http.StatusConflict:
					conflict++
				default:
					t.Fatalf("request %d status = %d, want %d or %d", i, status, http.StatusOK, http.StatusConflict)
				}
			}
			if ok != tc.wantOK {
				t.Errorf("%d requests answered %d, want %d", tc.requests, ok, tc.wantOK)
			}
			if conflict != tc.wantConflict {
				t.Errorf("%d requests answered %d, want %d", tc.requests, conflict, tc.wantConflict)
			}

			// The stored row must hold the quantity every successful adjustment added up to.
			got := doRequest(t, router, http.MethodGet, itemPath(item.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading item back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireItem(t, decodeItem(t, got), Item{
				ID:          item.ID,
				Name:        "widget",
				Description: "a thing",
				Quantity:    tc.wantFinalQuantity,
			})

			// One movement per successful adjustment, and no movement for a rejected one.
			rec := doRequest(t, router, http.MethodGet, movementsPath(item.ID), "")
			if rec.Code != http.StatusOK {
				t.Fatalf("listing movements: status = %d, body = %s", rec.Code, rec.Body.String())
			}
			movements := decodeMovements(t, rec)
			if len(movements) != tc.wantMovements {
				t.Fatalf("got %d movements, want %d (body = %s)", len(movements), tc.wantMovements, rec.Body.String())
			}
			if len(movements) != ok {
				t.Errorf("movements = %d, successful adjustments = %d; want one movement per success", len(movements), ok)
			}
			for i, movement := range movements {
				if movement.ItemID != item.ID {
					t.Errorf("row %d item_id = %d, want %d", i, movement.ItemID, item.ID)
				}
				if movement.Delta != tc.delta {
					t.Errorf("row %d delta = %d, want %d", i, movement.Delta, tc.delta)
				}
			}
		})
	}
}
