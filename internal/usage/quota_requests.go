package usage

import (
	"context"
	"crypto/sha256"
	"switch-codex/internal/store"
	"time"
)

type quotaKey struct {
	id     string
	digest [sha256.Size]byte
	epoch  uint64
}

type quotaFlight struct {
	done   chan struct{}
	result AccountQuota
}

// InvalidateQuotas keeps a refresh after an execution or redemption from joining
// a request that started before that operation changed the server's usage.
func (c *Client) InvalidateQuotas(ids []string) {
	c.quotaMu.Lock()
	defer c.quotaMu.Unlock()
	if c.quotaEpochs == nil {
		c.quotaEpochs = make(map[string]uint64)
	}
	for _, id := range ids {
		c.quotaEpochs[id]++
	}
}

func (c *Client) queryQuota(ctx context.Context, account store.Snapshot) AccountQuota {
	raw, credentialErr := account.Credentials()
	if credentialErr != nil {
		result := quotaError(account, credentialErr.Error())
		result.RequestID = c.quotaSequence.Add(1)
		return result
	}
	key := quotaKey{id: account.ID, digest: sha256.Sum256(raw)}
	c.quotaMu.Lock()
	key.epoch = c.quotaEpochs[account.ID]
	if flight := c.quotaFlights[key]; flight != nil {
		c.quotaMu.Unlock()
		select {
		case <-flight.done:
			return flight.result
		case <-ctx.Done():
			return quotaError(account, "额度查询已取消")
		}
	}
	if c.quotaFlights == nil {
		c.quotaFlights = make(map[quotaKey]*quotaFlight)
	}
	flight := &quotaFlight{done: make(chan struct{})}
	c.quotaFlights[key] = flight
	requestID := c.quotaSequence.Add(1)
	c.quotaMu.Unlock()

	var result AccountQuota
	if !c.waitQuotaSlot(ctx) {
		result = quotaError(account, "额度查询已取消")
	} else {
		result = c.accountQuota(ctx, account)
	}
	result.RequestID = requestID
	c.quotaMu.Lock()
	flight.result = result
	delete(c.quotaFlights, key)
	close(flight.done)
	c.quotaMu.Unlock()
	return result
}

// All query sources share the same start interval; HTTP work happens outside
// this gate, and cancellation also interrupts callers waiting for a slot.
func (c *Client) waitQuotaSlot(ctx context.Context) bool {
	select {
	case c.quotaGate <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-c.quotaGate }()
	if ctx.Err() != nil {
		return false
	}
	if delay := time.Until(c.nextQuotaStart); delay > 0 && c.QuotaInterval > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return false
		}
	}
	c.nextQuotaStart = time.Now().Add(c.QuotaInterval)
	return true
}
