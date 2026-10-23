package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

// decodeTask decodes a response body into a Task, so a changed JSON shape fails the test.
func decodeTask(t *testing.T, rec *httptest.ResponseRecorder) Task {
	t.Helper()
	var task Task
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatalf("decoding task from %q: %v", rec.Body.String(), err)
	}
	return task
}

// decodeTasks decodes a response body into a slice of Tasks.
func decodeTasks(t *testing.T, rec *httptest.ResponseRecorder) []Task {
	t.Helper()
	var tasks []Task
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decoding tasks from %q: %v", rec.Body.String(), err)
	}
	return tasks
}

// errorMessage returns the message carried by a writeError body.
func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding error body %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

// taskPath builds the path of one task, the way a client would ask for it.
func taskPath(id uint) string {
	return "/tasks/" + strconv.FormatUint(uint64(id), 10)
}

// taskStatusPath builds the path of one task's status change.
func taskStatusPath(id uint) string {
	return taskPath(id) + "/status"
}

// requireTask compares the fields a response is expected to echo back.
func requireTask(t *testing.T, got, want Task) {
	t.Helper()
	if want.ID != 0 && got.ID != want.ID {
		t.Errorf("id = %d, want %d", got.ID, want.ID)
	}
	if got.ID == 0 {
		t.Error("id = 0, want a generated id")
	}
	if got.Title != want.Title {
		t.Errorf("title = %q, want %q", got.Title, want.Title)
	}
	if got.Status != want.Status {
		t.Errorf("status = %q, want %q", got.Status, want.Status)
	}
}

// mustCreateTask creates a task through the API from a raw request body, so a test can start
// from a known row.
func mustCreateTask(t *testing.T, router *gin.Engine, body string) Task {
	t.Helper()
	rec := doRequest(t, router, http.MethodPost, "/tasks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating task: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return decodeTask(t, rec)
}

func TestCreateTask(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantTask   Task
	}{
		{
			name:       "omitted status defaults to open",
			body:       `{"title":"write plan"}`,
			wantStatus: http.StatusCreated,
			wantTask:   Task{Title: "write plan", Status: StatusOpen},
		},
		{
			name:       "explicit done status is stored",
			body:       `{"title":"ship it","status":"DONE"}`,
			wantStatus: http.StatusCreated,
			wantTask:   Task{Title: "ship it", Status: StatusDone},
		},
		{
			name:       "explicit in progress status is stored",
			body:       `{"title":"ongoing","status":"IN_PROGRESS"}`,
			wantStatus: http.StatusCreated,
			wantTask:   Task{Title: "ongoing", Status: StatusInProgress},
		},
		{
			name:       "explicit null defaults to open",
			body:       `{"title":"careful","status":null}`,
			wantStatus: http.StatusCreated,
			wantTask:   Task{Title: "careful", Status: StatusOpen},
		},
		{
			name:       "empty status",
			body:       `{"title":"x","status":""}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown status",
			body:       `{"title":"x","status":"ARCHIVED"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing title",
			body:       `{"status":"DONE"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "blank title",
			body:       `{"title":"   "}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			body:       `{"title":`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)

			rec := doRequest(t, router, http.MethodPost, "/tasks", tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusCreated {
				requireError(t, rec)

				// A rejected create must not leave a row behind.
				list := doRequest(t, router, http.MethodGet, "/tasks", "")
				if list.Code != http.StatusOK {
					t.Fatalf("listing tasks: status = %d, body = %s", list.Code, list.Body.String())
				}
				if got := list.Body.String(); got != `[]` {
					t.Errorf("tasks after a rejected create = %s, want []", got)
				}
				return
			}

			created := decodeTask(t, rec)
			requireTask(t, created, tc.wantTask)

			// The stored row, not just the response, must be what the case expects.
			got := doRequest(t, router, http.MethodGet, taskPath(created.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading task back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireTask(t, decodeTask(t, got), Task{
				ID:     created.ID,
				Title:  tc.wantTask.Title,
				Status: tc.wantTask.Status,
			})
		})
	}
}

func TestListTasks(t *testing.T) {
	tests := []struct {
		name       string
		seed       []string
		query      string
		wantStatus int
		wantBody   string
		want       []Task
	}{
		{
			name:       "empty database is an empty array",
			wantStatus: http.StatusOK,
			wantBody:   `[]`,
		},
		{
			name: "returns every created task",
			seed: []string{
				`{"title":"first"}`,
				`{"title":"second","status":"DONE"}`,
			},
			wantStatus: http.StatusOK,
			want: []Task{
				{Title: "first", Status: StatusOpen},
				{Title: "second", Status: StatusDone},
			},
		},
		{
			name: "filters by status",
			seed: []string{
				`{"title":"first"}`,
				`{"title":"second","status":"DONE"}`,
			},
			query:      "?status=DONE",
			wantStatus: http.StatusOK,
			want: []Task{
				{Title: "second", Status: StatusDone},
			},
		},
		{
			name: "unknown filter value",
			seed: []string{
				`{"title":"first"}`,
			},
			query:      "?status=ARCHIVED",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "empty filter value",
			seed: []string{
				`{"title":"first"}`,
			},
			query:      "?status=",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			for _, body := range tc.seed {
				mustCreateTask(t, router, body)
			}

			rec := doRequest(t, router, http.MethodGet, "/tasks"+tc.query, "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				return
			}
			if tc.wantBody != "" {
				if got := rec.Body.String(); got != tc.wantBody {
					t.Fatalf("body = %s, want %s", got, tc.wantBody)
				}
				return
			}

			tasks := decodeTasks(t, rec)
			if len(tasks) != len(tc.want) {
				t.Fatalf("got %d tasks, want %d (body = %s)", len(tasks), len(tc.want), rec.Body.String())
			}
			byTitle := make(map[string]Task, len(tasks))
			for _, task := range tasks {
				byTitle[task.Title] = task
			}
			for _, want := range tc.want {
				got, ok := byTitle[want.Title]
				if !ok {
					t.Fatalf("tasks = %s, want one titled %q", rec.Body.String(), want.Title)
				}
				requireTask(t, got, want)
			}
		})
	}
}

func TestGetTask(t *testing.T) {
	tests := []struct {
		name       string
		path       func(id uint) string
		wantStatus int
		wantTask   Task
	}{
		{
			name:       "existing task",
			path:       taskPath,
			wantStatus: http.StatusOK,
			wantTask:   Task{Title: "widget", Status: StatusInProgress},
		},
		{
			name:       "missing task",
			path:       fixedPath("/tasks/999"),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "non-numeric id",
			path:       fixedPath("/tasks/abc"),
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := mustCreateTask(t, router, `{"title":"widget","status":"IN_PROGRESS"}`)

			rec := doRequest(t, router, http.MethodGet, tc.path(seeded.ID), "")

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				return
			}
			requireTask(t, decodeTask(t, rec), Task{
				ID:     seeded.ID,
				Title:  tc.wantTask.Title,
				Status: tc.wantTask.Status,
			})
		})
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	// wantTask is the state the task must hold afterwards, read back with a follow-up GET. It is
	// nil when the task is not expected to exist. wantError, when set, is the exact message the
	// 400 body must carry; the invalid statuses all share the one message.
	tests := []struct {
		name       string
		path       func(id uint) string
		body       string
		wantStatus int
		wantTask   *Task
		wantError  string
	}{
		{
			name:       "changes the status",
			path:       taskStatusPath,
			body:       `{"status":"DONE"}`,
			wantStatus: http.StatusOK,
			wantTask:   &Task{Title: "widget", Status: StatusDone},
		},
		{
			name:       "unknown status",
			path:       taskStatusPath,
			body:       `{"status":"ARCHIVED"}`,
			wantStatus: http.StatusBadRequest,
			wantTask:   &Task{Title: "widget", Status: StatusOpen},
			wantError:  invalidStatusError().Error(),
		},
		{
			name:       "empty status",
			path:       taskStatusPath,
			body:       `{"status":""}`,
			wantStatus: http.StatusBadRequest,
			wantTask:   &Task{Title: "widget", Status: StatusOpen},
			wantError:  invalidStatusError().Error(),
		},
		{
			name:       "missing status key",
			path:       taskStatusPath,
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantTask:   &Task{Title: "widget", Status: StatusOpen},
			wantError:  invalidStatusError().Error(),
		},
		{
			name:       "null status",
			path:       taskStatusPath,
			body:       `{"status":null}`,
			wantStatus: http.StatusBadRequest,
			wantTask:   &Task{Title: "widget", Status: StatusOpen},
			wantError:  invalidStatusError().Error(),
		},
		{
			name:       "missing task",
			path:       fixedPath("/tasks/999/status"),
			body:       `{"status":"DONE"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing task wins over an invalid body",
			path:       fixedPath("/tasks/999/status"),
			body:       `{}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "non-numeric id",
			path:       fixedPath("/tasks/abc/status"),
			body:       `{"status":"DONE"}`,
			wantStatus: http.StatusBadRequest,
			wantTask:   &Task{Title: "widget", Status: StatusOpen},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := newTestRouter(t)
			seeded := mustCreateTask(t, router, `{"title":"widget"}`)

			rec := doRequest(t, router, http.MethodPatch, tc.path(seeded.ID), tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				requireError(t, rec)
				if tc.wantError != "" {
					if got := errorMessage(t, rec); got != tc.wantError {
						t.Errorf("error message = %q, want %q", got, tc.wantError)
					}
				}
			}
			if tc.wantStatus == http.StatusOK {
				requireTask(t, decodeTask(t, rec), Task{
					ID:     seeded.ID,
					Title:  tc.wantTask.Title,
					Status: tc.wantTask.Status,
				})
			}

			if tc.wantTask == nil {
				return
			}

			// The stored row, not just the response, must be what the case expects.
			got := doRequest(t, router, http.MethodGet, taskPath(seeded.ID), "")
			if got.Code != http.StatusOK {
				t.Fatalf("reading task back: status = %d, body = %s", got.Code, got.Body.String())
			}
			requireTask(t, decodeTask(t, got), Task{
				ID:     seeded.ID,
				Title:  tc.wantTask.Title,
				Status: tc.wantTask.Status,
			})
		})
	}
}
