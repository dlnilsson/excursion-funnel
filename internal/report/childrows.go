package report

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/provider"
)

// maxRequestWindow bounds how far the expanding scan will reach back before
// giving up on filling the caller's limit.
const maxRequestWindow = 1 << 16

// expandingJoin gathers child rows (tool calls, web requests) belonging to
// recent requests.
//
// It deliberately avoids a SQL JOIN between requests and the child table: a
// Quack-attached remote catalog rejects queries that stream-scan two tables at
// once ("Multiple streaming scans... not currently supported"), which such a
// JOIN triggers. Instead it fetches candidate requests and their children as
// two independent single-table scans and joins in Go.
//
// Because a request may have no children at all, a window of `limit` requests
// can yield fewer than `limit` children. The window therefore doubles until the
// limit is met, the requests are exhausted, or maxRequestWindow is reached.
func expandingJoin[T any](
	ctx context.Context,
	r *Reporter,
	where string,
	args []any,
	limit int,
	join func(context.Context, []toolCallRequest, int) ([]T, error),
) ([]T, error) {
	for requestWindow := limit; ; requestWindow *= 2 {
		requests, err := r.candidateRequests(ctx, where, args, requestWindow)
		if err != nil {
			return nil, err
		}
		out, err := join(ctx, requests, limit)
		if err != nil {
			return nil, err
		}
		if len(out) >= limit || len(requests) < requestWindow || requestWindow >= maxRequestWindow {
			return out, nil
		}
	}
}

// childRowQuery describes how to read one child table and stitch its rows onto
// their parent requests.
type childRowQuery[T any] struct {
	// label names the result set in error messages.
	label string
	// sql selects (request_id, …) from the child table, without a WHERE clause.
	sql string
	// filter is an extra SQL predicate ANDed onto the request_id IN (…) clause.
	// Filtering in SQL rather than in Go matters here: the child scan can run
	// against a remote Quack catalog, where every excluded row is wire traffic.
	filter string
	// scan reads one row, returning its parent request id.
	scan func(*sql.Rows, *T) (requestID string, err error)
	// keep filters rows after scanning. A nil keep retains everything.
	keep func(T) bool
	// attach copies parent request metadata onto the child row.
	attach func(*T, toolCallRequest)
	// ordinal orders rows within one request, preserving provider event order.
	ordinal func(T) int
}

// joinChildRows fetches every child row for requests and returns them ordered
// by request (newest first, as candidateRequests ordered them) then by ordinal,
// truncated to limit.
func joinChildRows[T any](ctx context.Context, r *Reporter, requests []toolCallRequest, limit int, q childRowQuery[T]) ([]T, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	byID := make(map[string]toolCallRequest, len(requests))
	ids := make([]any, len(requests))
	for i, req := range requests {
		byID[req.ID] = req
		ids[i] = req.ID
	}

	byRequest := make(map[string][]T, len(requests))
	query := q.sql + " WHERE request_id IN (" + placeholders(len(ids)) + ")"
	if q.filter != "" {
		query += " AND " + q.filter
	}
	if _, err := queryRowsInto(ctx, r.store.DB(), q.label, query, ids, func(rows *sql.Rows, row *T) (bool, error) {
		requestID, err := q.scan(rows, row)
		if err != nil {
			return false, err
		}
		if q.keep != nil && !q.keep(*row) {
			return false, nil
		}
		q.attach(row, byID[requestID])
		byRequest[requestID] = append(byRequest[requestID], *row)
		return false, nil
	}); err != nil {
		return nil, err
	}

	out := make([]T, 0, limit)
	for _, req := range requests {
		children := byRequest[req.ID]
		sort.Slice(children, func(i, j int) bool { return q.ordinal(children[i]) < q.ordinal(children[j]) })
		out = append(out, children...)
	}
	return out[:min(len(out), limit)], nil
}

type toolCallRequest struct {
	ID        string
	Source    string
	StartedAt time.Time
	Provider  string
	Client    string
	Model     string
}

// candidateRequests reads the newest requests matching where, to be joined
// against a child table.
func (r *Reporter) candidateRequests(ctx context.Context, where string, whereArgs []any, limit int) ([]toolCallRequest, error) {
	args := append(slices.Clone(whereArgs), limit)
	return queryRows(ctx, r.store.DB(), "candidate requests", `
SELECT id, COALESCE(source, 'unknown'), started_at, `+provider.SQLForPath("path")+`, `+provider.ClientSQL()+`,
  COALESCE(model_reported, model_requested, 'unknown')
FROM requests
`+where+`
ORDER BY started_at DESC LIMIT ?`, args, func(rows *sql.Rows, req *toolCallRequest) error {
		return rows.Scan(&req.ID, &req.Source, &req.StartedAt, &req.Provider, &req.Client, &req.Model)
	})
}
