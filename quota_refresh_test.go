package main

import (
	"context"
	"reflect"
	"switch-codex/internal/usage"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuotaRefresherQueuesWithoutWaitingAndCombinesPendingAccounts(t *testing.T) {
	started := make(chan []string, 2)
	release := make(chan struct{})
	emitted := make(chan usage.AccountQuotas, 2)
	r := newQuotaRefresher(context.Background(), func(ctx context.Context, ids []string) (usage.AccountQuotas, error) {
		started <- ids
		select {
		case <-release:
		case <-ctx.Done():
			return usage.AccountQuotas{}, ctx.Err()
		}
		return usage.AccountQuotas{Revision: 1}, nil
	}, func(result usage.AccountQuotas) { emitted <- result })
	defer r.Close()
	r.Queue([]string{"a"})
	select {
	case ids := <-started:
		if !reflect.DeepEqual(ids, []string{"a"}) {
			t.Fatal(ids)
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not start in the background")
	}
	queued := make(chan struct{})
	go func() {
		r.Queue([]string{"b", "c"})
		r.Queue([]string{"b"})
		close(queued)
	}()
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("queue waited for the ongoing HTTP work")
	}
	close(release)
	select {
	case ids := <-started:
		if !reflect.DeepEqual(ids, []string{"b", "c"}) {
			t.Fatalf("pending work was not merged: %v", ids)
		}
	case <-time.After(time.Second):
		t.Fatal("pending work was lost")
	}
	for range 2 {
		select {
		case <-emitted:
		case <-time.After(time.Second):
			t.Fatal("successful query did not emit")
		}
	}
}

func TestQuotaRefresherCloseCancelsWaitsAndRejectsNewWork(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	var calls, emissions atomic.Int32
	r := newQuotaRefresher(context.Background(), func(ctx context.Context, ids []string) (usage.AccountQuotas, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(finished)
		return usage.AccountQuotas{}, nil
	}, func(usage.AccountQuotas) { emissions.Add(1) })
	r.Queue([]string{"a"})
	<-started
	r.Queue([]string{"b"})
	r.Close()
	select {
	case <-finished:
	default:
		t.Fatal("close did not wait for background work")
	}
	r.Queue([]string{"c"})
	r.Close()
	if calls.Load() != 1 || emissions.Load() != 0 {
		t.Fatal("cancelled or closed refresher emitted or accepted new work")
	}
}
