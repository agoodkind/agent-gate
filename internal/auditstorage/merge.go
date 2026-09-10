package auditstorage

import (
	"context"
	"errors"
)

// QueryPageSize bounds each bucket's buffered summary rows.
const QueryPageSize = 100

// QuerySummary carries only the keys needed to merge and select one record.
type QuerySummary struct {
	ID       string
	Time     string
	Sequence int64
}

// ValidatePage rejects unsupported public pagination arguments.
func ValidatePage(limit, offset int) error {
	if limit < 0 || limit > 1000 {
		return errors.New("query limit must be between 0 and 1000")
	}
	if offset < 0 {
		return errors.New("query offset must not be negative")
	}
	return nil
}

type mergePage struct {
	handle   *BucketHandle
	rows     []QuerySummary
	position int
}

type summaryHeap []*mergePage

func (heap *summaryHeap) less(left, right int) bool {
	values := *heap
	a, b := values[left].rows[values[left].position], values[right].rows[values[right].position]
	if a.Time != b.Time {
		return a.Time > b.Time
	}
	if a.Sequence != b.Sequence {
		return a.Sequence > b.Sequence
	}
	if a.Sequence == 0 && a.ID != b.ID {
		return a.ID > b.ID
	}
	return values[left].handle.Bucket.ID > values[right].handle.Bucket.ID
}

func (heap *summaryHeap) push(item *mergePage) {
	*heap = append(*heap, item)
	for index := len(*heap) - 1; index > 0; {
		parent := (index - 1) / 2
		if !heap.less(index, parent) {
			break
		}
		(*heap)[index], (*heap)[parent] = (*heap)[parent], (*heap)[index]
		index = parent
	}
}

func (heap *summaryHeap) pop() *mergePage {
	item := (*heap)[0]
	(*heap)[0] = (*heap)[len(*heap)-1]
	*heap = (*heap)[:len(*heap)-1]
	for index := 0; index*2+1 < len(*heap); {
		child := index*2 + 1
		if child+1 < len(*heap) && heap.less(child+1, child) {
			child++
		}
		if !heap.less(child, index) {
			break
		}
		(*heap)[index], (*heap)[child] = (*heap)[child], (*heap)[index]
		index = child
	}
	return item
}

// Merge walks bounded keyset pages, applying offset and limit across all buckets.
// Summary timestamps must use FormatTime; bucket identity breaks equal local keys.
func Merge(ctx context.Context, set *ReadSet, limit, offset int,
	page func(*BucketHandle, *QuerySummary) ([]QuerySummary, error),
	yield func(*BucketHandle, QuerySummary) error,
) error {
	var heap summaryHeap
	for _, handle := range set.Handles {
		if err := ctx.Err(); err != nil {
			return storageError("initialize query merge", err)
		}
		rows, err := page(handle, nil)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			heap.push(&mergePage{handle: handle, rows: rows, position: 0})
		}
	}
	count := 0
	for len(heap) > 0 {
		if err := ctx.Err(); err != nil {
			return storageError("merge query pages", err)
		}
		item := heap.pop()
		row := item.rows[item.position]
		if offset > 0 {
			offset--
		} else {
			if err := yield(item.handle, row); err != nil {
				return err
			}
			count++
			if limit > 0 && count >= limit {
				return nil
			}
		}
		item.position++
		if item.position == len(item.rows) {
			if len(item.rows) < QueryPageSize {
				continue
			}
			rows, err := page(item.handle, &row)
			if err != nil {
				return err
			}
			item.rows, item.position = rows, 0
		}
		if len(item.rows) > 0 {
			heap.push(item)
		}
	}
	return nil
}
