package main

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	steamConcurrency = 4
	storeBatchSize   = 50
	batchTimeout     = 6 * time.Second
)

type batchResult[T any] struct {
	value T
	issue problem
}

// Each worker starts a new deadline when it takes a batch. Results keep input
// order, and a failed batch cannot cancel work on its siblings.
func runBatches[I, T any](ctx context.Context, items []I, size int, timeout time.Duration, fetch func(context.Context, []I) (T, problem)) []batchResult[T] {
	batches := slices.Collect(slices.Chunk(items, size))
	results := make([]batchResult[T], len(batches))
	var next atomic.Int32
	var workers sync.WaitGroup
	for range min(steamConcurrency, len(batches)) {
		workers.Go(func() {
			for i := int(next.Add(1) - 1); i < len(batches); i = int(next.Add(1) - 1) {
				if err := ctx.Err(); err != nil {
					results[i].issue = sanitizeError(err)
					continue
				}
				batchCtx, cancel := context.WithTimeout(ctx, timeout)
				results[i].value, results[i].issue = fetch(batchCtx, batches[i])
				cancel()
			}
		})
	}
	workers.Wait()
	return results
}
