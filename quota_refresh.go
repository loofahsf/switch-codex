package main

import (
	"context"
	"sort"
	"switch-codex/internal/usage"
	"sync"
)

// quotaRefresher combines pending account refreshes and owns their background
// goroutine. Queue never waits for an HTTP request.
type quotaRefresher struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	query    func(context.Context, []string) (usage.AccountQuotas, error)
	emit     func(usage.AccountQuotas)
	pending  map[string]struct{}
	running  bool
	stopping bool
	wg       sync.WaitGroup
}

func newQuotaRefresher(ctx context.Context, query func(context.Context, []string) (usage.AccountQuotas, error), emit func(usage.AccountQuotas)) *quotaRefresher {
	ctx, cancel := context.WithCancel(ctx)
	return &quotaRefresher{ctx: ctx, cancel: cancel, query: query, emit: emit, pending: make(map[string]struct{})}
}

func (r *quotaRefresher) Queue(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping || r.ctx.Err() != nil {
		return
	}
	for _, id := range ids {
		r.pending[id] = struct{}{}
	}
	if r.running || len(r.pending) == 0 {
		return
	}
	r.running = true
	r.wg.Add(1)
	go r.run()
}

func (r *quotaRefresher) run() {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		if r.stopping || r.ctx.Err() != nil || len(r.pending) == 0 {
			r.running = false
			r.mu.Unlock()
			return
		}
		ids := make([]string, 0, len(r.pending))
		for id := range r.pending {
			ids = append(ids, id)
		}
		clear(r.pending)
		r.mu.Unlock()
		sort.Strings(ids)
		result, err := r.query(r.ctx, ids)
		if err == nil && r.ctx.Err() == nil {
			r.emit(result)
		}
	}
}

func (r *quotaRefresher) Close() {
	r.mu.Lock()
	r.stopping = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
}
