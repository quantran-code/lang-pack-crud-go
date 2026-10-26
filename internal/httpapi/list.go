package httpapi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TotalCountHeader carries the number of rows matching the list filters before limit and offset
// are applied. It is present on every successful list response, including an empty page.
const TotalCountHeader = "X-Total-Count"

// The page size bounds and the start of the first page. A request that leaves limit or offset out
// takes these values; a present but out-of-range value is a 400.
const (
	defaultLimit  = 20
	maxLimit      = 100
	defaultOffset = 0
)

// The two accepted order values. Matching is case-sensitive: "ASC" is not "asc".
const (
	orderAsc  = "asc"
	orderDesc = "desc"
)

// The one message every invalid value of a parameter produces. The sort message is not here
// because its accepted values differ per resource; see listSpec.invalidSortError.
const (
	limitMessage  = "limit must be an integer from 1 to 100"
	offsetMessage = "offset must be zero or greater"
	orderMessage  = "order must be asc or desc"
)

// sortColumn binds a sort key a client may send (Key) to the column it orders by (Column). The
// column name is held in a struct from the fixed per-resource list, so no part of a query string
// is ever concatenated into the SQL.
type sortColumn struct {
	Key    string `json:"-"`
	Column string
}

// listSpec is the fixed, ordered list of sortable keys of one resource. It is the only place the
// accepted sort values and the wording of that resource's sort error are written down.
type listSpec []sortColumn

// column maps a sort key to its column. The lookup is exact and case-sensitive; an unknown or
// empty key reports ok == false.
func (s listSpec) column(key string) (column string, ok bool) {
	for _, candidate := range s {
		if candidate.Key == key {
			return candidate.Column, true
		}
	}
	return "", false
}

// invalidSortError builds the single message every invalid sort produces, from the spec's keys,
// so no handler repeats the accepted values.
func (s listSpec) invalidSortError() error {
	keys := make([]string, 0, len(s))
	for _, candidate := range s {
		keys = append(keys, candidate.Key)
	}
	return fmt.Errorf("sort must be one of %s", strings.Join(keys, ", "))
}

// itemListSpec is the sortable keys of GET /items. description is deliberately not sortable.
var itemListSpec = listSpec{
	{Key: "id", Column: "id"},
	{Key: "name", Column: "name"},
	{Key: "quantity", Column: "quantity"},
}

// taskListSpec is the sortable keys of GET /tasks.
var taskListSpec = listSpec{
	{Key: "id", Column: "id"},
	{Key: "title", Column: "title"},
	{Key: "status", Column: "status"},
}

// listParams is the parsed list query of one request. Column always comes from the resource's
// listSpec, never from the query string directly.
type listParams struct {
	Limit  int
	Offset int
	Column string
	Desc   bool
}

// parseListParams reads limit, offset, sort and order from the request. GetQuery tells an absent
// parameter, which takes its default, apart from a present but empty one, which is invalid. The
// parameters are validated in the fixed order limit, offset, sort, order and the first failure is
// returned, so a request with several bad values reports the earliest of them.
func parseListParams(c *gin.Context, spec listSpec) (listParams, error) {
	params := listParams{
		Limit:  defaultLimit,
		Offset: defaultOffset,
	}
	if column, ok := spec.column("id"); ok {
		params.Column = column
	}

	if raw, ok := c.GetQuery("limit"); ok {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxLimit {
			return listParams{}, errors.New(limitMessage)
		}
		params.Limit = limit
	}

	if raw, ok := c.GetQuery("offset"); ok {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return listParams{}, errors.New(offsetMessage)
		}
		params.Offset = offset
	}

	if raw, ok := c.GetQuery("sort"); ok {
		column, found := spec.column(raw)
		if !found {
			return listParams{}, spec.invalidSortError()
		}
		params.Column = column
	}

	if raw, ok := c.GetQuery("order"); ok {
		switch raw {
		case orderAsc:
			params.Desc = false
		case orderDesc:
			params.Desc = true
		default:
			return listParams{}, errors.New(orderMessage)
		}
	}

	return params, nil
}

// listPage loads one page of rows into dest and returns the number of rows that matched before
// limit and offset were applied, which is what the X-Total-Count header reports.
//
// base builds the resource's already-filtered query and is called twice, so the count and the page
// never share a statement. The page is ordered by the column from the resource's listSpec, then by
// id ascending so rows with equal sort values keep a stable order; a primary order of id DESC is
// still decided by that first clause. Both orderings are clause.OrderByColumn values, so the column
// name is an identifier held in a struct rather than SQL text.
func listPage(base func() *gorm.DB, params listParams, dest any) (int64, error) {
	var total int64
	if err := base().Model(dest).Count(&total).Error; err != nil {
		return 0, fmt.Errorf("counting rows: %w", err)
	}

	err := base().
		Order(clause.OrderByColumn{Column: clause.Column{Name: params.Column}, Desc: params.Desc}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "id"}, Desc: false}).
		Limit(params.Limit).
		Offset(params.Offset).
		Find(dest).Error
	if err != nil {
		return 0, fmt.Errorf("listing rows: %w", err)
	}

	return total, nil
}
