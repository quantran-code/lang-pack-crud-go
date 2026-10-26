package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// requireTaskIDs asserts got carries the same ids, in the same order, as want.
func requireTaskIDs(t *testing.T, got, want []Task) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d tasks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("row %d has id %d, want %d", i, got[i].ID, want[i].ID)
		}
	}
}

// seedTasks creates open+inProgress+done tasks through the API, one status group after another, and
// returns them in creation order so their ids ascend: the OPEN tasks come first, then the
// IN_PROGRESS ones, then the DONE ones. The last two OPEN tasks share one title, which makes the id
// tie-break observable when sorting by title.
func seedTasks(t *testing.T, router *gin.Engine, open, inProgress, done int) []Task {
	t.Helper()
	tasks := make([]Task, 0, open+inProgress+done)
	create := func(title string, status Status) {
		body := fmt.Sprintf(`{"title":%q,"status":%q}`, title, status)
		tasks = append(tasks, mustCreateTask(t, router, body))
	}

	for i := 0; i < open; i++ {
		if open >= 2 && i >= open-2 {
			create("task-tie", StatusOpen)
			continue
		}
		create(fmt.Sprintf("open-%02d", i), StatusOpen)
	}
	for i := 0; i < inProgress; i++ {
		create(fmt.Sprintf("doing-%02d", i), StatusInProgress)
	}
	for i := 0; i < done; i++ {
		create(fmt.Sprintf("done-%02d", i), StatusDone)
	}
	return tasks
}

func TestListTasksPagination(t *testing.T) {
	const (
		openCount       = 10
		inProgressCount = 8
		doneCount       = 7
	)

	tests := []struct {
		name      string
		query     string
		wantTotal string
		wantBody  string
		check     func(t *testing.T, seeded, got []Task)
	}{
		{
			name:      "defaults return the first page in id order",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Task) {
				t.Helper()
				requireTaskIDs(t, got, seeded[:defaultLimit])
			},
		},
		{
			name:      "status filter is counted before limit and offset",
			query:     "?status=OPEN&limit=5&offset=5",
			wantTotal: "10",
			check: func(t *testing.T, seeded, got []Task) {
				t.Helper()
				requireTaskIDs(t, got, seeded[5:10])
				for _, task := range got {
					if task.Status != StatusOpen {
						t.Fatalf("task %d has status %q, want %q", task.ID, task.Status, StatusOpen)
					}
				}
			},
		},
		{
			name:      "offset past the last matching row is an empty array",
			query:     "?status=DONE&offset=7",
			wantTotal: "7",
			wantBody:  "[]",
		},
		{
			name:      "sort by title descending",
			query:     "?sort=title&order=desc",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Task) {
				t.Helper()
				if len(got) != defaultLimit {
					t.Fatalf("got %d tasks, want the default page of %d", len(got), defaultLimit)
				}
				for i := 1; i < len(got); i++ {
					if got[i].Title > got[i-1].Title {
						t.Fatalf("row %d has title %q, above the previous %q; want descending titles", i, got[i].Title, got[i-1].Title)
					}
				}
				// The two tasks sharing the highest title come first, ordered by id ascending.
				if got[0].Title != "task-tie" || got[1].Title != "task-tie" {
					t.Fatalf("first two titles = %q, %q, want both %q", got[0].Title, got[1].Title, "task-tie")
				}
				if got[0].ID >= got[1].ID {
					t.Errorf("ids of equal titles = %d, %d, want ascending", got[0].ID, got[1].ID)
				}
			},
		},
		{
			name:      "sort by status keeps ids ascending inside a status",
			query:     "?sort=status&limit=100",
			wantTotal: "25",
			check: func(t *testing.T, seeded, got []Task) {
				t.Helper()
				for i := 1; i < len(got); i++ {
					if got[i].Status < got[i-1].Status {
						t.Fatalf("row %d has status %q, below the previous %q; want ascending statuses", i, got[i].Status, got[i-1].Status)
					}
				}
				// DONE < IN_PROGRESS < OPEN on the wire, so an ascending status sort groups them in
				// that order, each group in id order.
				want := make([]Task, 0, len(seeded))
				want = append(want, seeded[openCount+inProgressCount:]...)
				want = append(want, seeded[openCount:openCount+inProgressCount]...)
				want = append(want, seeded[:openCount]...)
				requireTaskIDs(t, got, want)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := seedTasks(t, router, openCount, inProgressCount, doneCount)

			rec := doRequest(t, router, http.MethodGet, "/tasks"+tc.query, "")

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

			got := decodeTasks(t, rec)
			if tc.check != nil {
				tc.check(t, seeded, got)
			}
		})
	}
}

func TestListTasksInvalidParameters(t *testing.T) {
	// The exact wording every invalid parameter produces. The sort message lists this resource's
	// sortable keys, which differ from the items ones.
	const (
		wantLimit  = "limit must be an integer from 1 to 100"
		wantOffset = "offset must be zero or greater"
		wantSort   = "sort must be one of id, title, status"
		wantOrder  = "order must be asc or desc"
	)
	wantStatus := invalidStatusError().Error()

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
			name:    "item sort key is not a task sort key",
			query:   "?sort=name",
			wantMsg: wantSort,
		},
		{
			name:    "item quantity is not a task sort key",
			query:   "?sort=quantity",
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
			query:   "?limit=0&sort=name",
			wantMsg: wantLimit,
		},
		{
			name:    "offset is checked before order",
			query:   "?offset=-1&order=sideways",
			wantMsg: wantOffset,
		},
		{
			name:    "unknown status filter still fails",
			query:   "?status=ARCHIVED",
			wantMsg: wantStatus,
		},
		{
			name:    "empty status filter still fails",
			query:   "?status=",
			wantMsg: wantStatus,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seedTasks(t, router, 1, 1, 1)

			rec := doRequest(t, router, http.MethodGet, "/tasks"+tc.query, "")

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

			// The rejected request must not have changed anything: the seeded rows are still
			// there and still listable.
			list := doRequest(t, router, http.MethodGet, "/tasks", "")
			if list.Code != http.StatusOK {
				t.Fatalf("listing tasks after a rejected request: status = %d, body = %s", list.Code, list.Body.String())
			}
			if got := list.Header().Get(TotalCountHeader); got != "3" {
				t.Errorf("%s after a rejected request = %q, want %q", TotalCountHeader, got, "3")
			}
		})
	}
}
