package mimiry

import (
	"context"
	"iter"
	"net/url"
	"strconv"
	"time"
)

// ListOptions pages a list endpoint. Zero values take the server's defaults
// (limit 50, offset 0); the server clamps a limit outside [1, 100] to 50.
type ListOptions struct {
	Limit  int `json:"limit,omitzero"`
	Offset int `json:"offset,omitzero"`
}

func (o ListOptions) encode(q url.Values) {
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
}

// Page is one page of a list endpoint. TotalCount is the count after filters
// and before paging; Limit and Offset are the values the server applied.
// Unavailable counts entries the server knows of but could not read on this
// request; when it is not 0, the page is incomplete.
//
// As JSON it is the list shape of the API and of `mimiry rpc`.
type Page[T any] struct {
	Items       []T `json:"items"`
	TotalCount  int `json:"total_count"`
	Limit       int `json:"limit"`
	Offset      int `json:"offset"`
	Unavailable int `json:"unavailable"`
}

// maxPageSize is the server's upper bound on limit.
const maxPageSize = 100

// all iterates every item of a list endpoint, fetching pages of the largest
// size the server allows.
//
// Lists are newest first and paged by offset, so an item created while the
// iteration runs shifts the rest down one place and the next page repeats an
// item. Items are not de-duplicated here, because nothing about an item says
// it is the same one without knowing its type; callers that need exactly-once
// should key on the ID.
func all[T any](ctx context.Context, fetch func(context.Context, ListOptions) (*Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		offset := 0
		for {
			p, err := fetch(ctx, ListOptions{Limit: maxPageSize, Offset: offset})
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, it := range p.Items {
				if !yield(it, nil) {
					return
				}
			}
			offset += len(p.Items)
			if len(p.Items) == 0 || offset >= p.TotalCount {
				return
			}
		}
	}
}

// timeParam formats a time for a query parameter.
func timeParam(t time.Time) string { return t.UTC().Format(time.RFC3339) }
