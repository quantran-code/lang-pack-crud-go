package httpapi

import (
	"math"
	"net/http"
	"testing"
)

func TestParseID(t *testing.T) {
	// wantErr marks the malformed ids (a handler answers 400); an all-digit id above math.MaxInt64
	// is well formed but cannot identify a row, so it is ok == false with a nil error (404).
	tests := []struct {
		name    string
		raw     string
		wantID  uint64
		wantOK  bool
		wantErr bool
	}{
		{
			name:    "empty string",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "non-digit characters",
			raw:     "abc",
			wantErr: true,
		},
		{
			name:    "negative number",
			raw:     "-1",
			wantErr: true,
		},
		{
			name:    "fraction",
			raw:     "1.5",
			wantErr: true,
		},
		{
			name:   "zero is a valid id",
			raw:    "0",
			wantID: 0,
			wantOK: true,
		},
		{
			name:   "largest signed 64-bit value",
			raw:    "9223372036854775807",
			wantID: uint64(math.MaxInt64),
			wantOK: true,
		},
		{
			name:   "largest unsigned 64-bit value is not an id",
			raw:    "18446744073709551615",
			wantOK: false,
		},
		{
			name:   "value beyond unsigned 64-bit still parses as digits",
			raw:    "18446744073709551616",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok, err := parseID(tc.raw)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseID(%q) error = nil, want an error", tc.raw)
				}
			} else if err != nil {
				t.Fatalf("parseID(%q) error = %v, want nil", tc.raw, err)
			}
			if ok != tc.wantOK {
				t.Errorf("parseID(%q) ok = %t, want %t", tc.raw, ok, tc.wantOK)
			}
			if id != tc.wantID {
				t.Errorf("parseID(%q) id = %d, want %d", tc.raw, id, tc.wantID)
			}
		})
	}
}

func TestPathIDHandling(t *testing.T) {
	// A non-digit id is malformed, so it is the handler's existing 400. An all-digit id is well
	// formed, so an id that no stored row can carry is the 404 the handler already gives a missing
	// row — never the 500 that reaching SQLite with it used to produce.
	ids := []struct {
		id         string
		wantStatus int
	}{
		{id: "abc", wantStatus: http.StatusBadRequest},
		{id: "-1", wantStatus: http.StatusBadRequest},
		{id: "1.5", wantStatus: http.StatusBadRequest},
		{id: "9223372036854775807", wantStatus: http.StatusNotFound},
		{id: "18446744073709551615", wantStatus: http.StatusNotFound},
	}

	// The five handlers that take an :id, each with the messages it already answered with. The PUT
	// and PATCH bodies are otherwise valid, so the id alone decides the status.
	endpoints := []struct {
		name         string
		method       string
		path         func(id string) string
		body         string
		invalidIDMsg string
		notFoundMsg  string
	}{
		{
			name:         "get item",
			method:       http.MethodGet,
			path:         func(id string) string { return "/items/" + id },
			invalidIDMsg: "invalid item id",
			notFoundMsg:  "item not found",
		},
		{
			name:         "update item",
			method:       http.MethodPut,
			path:         func(id string) string { return "/items/" + id },
			body:         `{"name":"widget","quantity":1}`,
			invalidIDMsg: "invalid item id",
			notFoundMsg:  "item not found",
		},
		{
			name:         "delete item",
			method:       http.MethodDelete,
			path:         func(id string) string { return "/items/" + id },
			invalidIDMsg: "invalid item id",
			notFoundMsg:  "item not found",
		},
		{
			name:         "get task",
			method:       http.MethodGet,
			path:         func(id string) string { return "/tasks/" + id },
			invalidIDMsg: "invalid task id",
			notFoundMsg:  "task not found",
		},
		{
			name:         "update task status",
			method:       http.MethodPatch,
			path:         func(id string) string { return "/tasks/" + id + "/status" },
			body:         `{"status":"DONE"}`,
			invalidIDMsg: "invalid task id",
			notFoundMsg:  "task not found",
		},
	}

	for _, endpoint := range endpoints {
		for _, tc := range ids {
			t.Run(endpoint.name+"/"+tc.id, func(t *testing.T) {
				router := newTestRouter(t)

				rec := doRequest(t, router, endpoint.method, endpoint.path(tc.id), endpoint.body)

				if rec.Code == http.StatusInternalServerError {
					t.Fatalf("status = 500, want %d; an id must never be sent to the database as a 500 (body = %s)", tc.wantStatus, rec.Body.String())
				}
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tc.wantStatus, rec.Body.String())
				}
				requireError(t, rec)

				want := endpoint.notFoundMsg
				if tc.wantStatus == http.StatusBadRequest {
					want = endpoint.invalidIDMsg
				}
				if got := errorMessage(t, rec); got != want {
					t.Errorf("error message = %q, want %q", got, want)
				}
			})
		}
	}
}
