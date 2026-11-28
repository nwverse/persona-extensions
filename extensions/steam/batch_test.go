package main

import (
	"context"
	"testing"
	"time"
)

func TestRunBatchesDeadlineKeepsSiblingResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	items := []int{1, 2, 3, 4, 5}
	got := runBatches(ctx, items, 1, 100*time.Millisecond, func(ctx context.Context, batch []int) (int, problem) {
		if batch[0] == items[0] {
			<-ctx.Done()
			return 0, sanitizeError(ctx.Err())
		}
		return batch[0], ""
	})
	if ctx.Err() != nil || len(got) != len(items) || got[0].issue != problemTimeout {
		t.Fatalf("the hung batch should time out before its parent: %+v, parent %v", got, ctx.Err())
	}
	for i, result := range got[1:] {
		if result.value != items[i+1] || result.issue != "" {
			t.Fatalf("unexpected result from sibling batch %d: %+v", i+1, result)
		}
	}
}
