package httpapi

import (
	"errors"
	"math"
	"strconv"
)

// parseID parses the :id path parameter shared by every handler that takes one. Three outcomes
// are told apart, because the handlers answer each one differently:
//
//   - err != nil when raw is empty or holds a byte outside '0'..'9', so "abc", "-1" and "1.5" are
//     a malformed id and the handler answers 400 with its existing message.
//   - ok == false with a nil err when raw is all digits but larger than math.MaxInt64, including
//     values that overflow uint64. Such an id cannot identify a stored row, so the handler takes
//     its existing 404 path instead of sending it to the database and failing with a 500.
//   - ok == true when raw is all digits and fits, leaving the handler to look the row up.
//
// id is always 0 when ok is false or err is non-nil.
func parseID(raw string) (id uint64, ok bool, err error) {
	if raw == "" {
		return 0, false, errors.New("id is empty")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false, errors.New("id is not a number")
		}
	}

	parsed, parseErr := strconv.ParseUint(raw, 10, 64)
	if parseErr != nil || parsed > math.MaxInt64 {
		return 0, false, nil
	}
	return parsed, true, nil
}
