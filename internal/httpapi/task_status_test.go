package httpapi

import "testing"

func TestStatusValid(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		want   bool
	}{
		{
			name:   "open is valid",
			status: StatusOpen,
			want:   true,
		},
		{
			name:   "in progress is valid",
			status: StatusInProgress,
			want:   true,
		},
		{
			name:   "done is valid",
			status: StatusDone,
			want:   true,
		},
		{
			name:   "wire value of open is valid",
			status: "OPEN",
			want:   true,
		},
		{
			name:   "wire value of in progress is valid",
			status: "IN_PROGRESS",
			want:   true,
		},
		{
			name:   "wire value of done is valid",
			status: "DONE",
			want:   true,
		},
		{
			name:   "empty string is invalid",
			status: "",
			want:   false,
		},
		{
			name:   "lowercase is invalid",
			status: "open",
			want:   false,
		},
		{
			name:   "unknown value is invalid",
			status: "ARCHIVED",
			want:   false,
		},
		{
			name:   "trailing space is invalid",
			status: " DONE",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Valid(); got != tc.want {
				t.Errorf("Status(%q).Valid() = %t, want %t", string(tc.status), got, tc.want)
			}
		})
	}
}
