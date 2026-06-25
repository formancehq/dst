package main

import "context"

// registerInflight reserves a ticket and records the operation. Must run BEFORE
// dispatch: the ticket is the dispatch order tryDrain relies on, and the
// operation is what the serialization search folds. Caller holds c.mu.
func (c *Checker) registerInflight(op Operation) uint64 {
	t := c.ticketSeq.Add(1)
	c.inflight[t] = op

	return t
}

// removeInflight drops a ticket. Caller holds c.mu.
func (c *Checker) removeInflight(ticket uint64) {
	delete(c.inflight, ticket)
}

// registerRead reserves a ticket for an outstanding read. Holding it gates
// draining (tryDrain). Caller holds c.mu.
func (c *Checker) registerRead() uint64 {
	t := c.ticketSeq.Add(1)
	c.reads[t] = struct{}{}

	return t
}

// finishRead drops an outstanding read and resumes any draining it held back.
func (c *Checker) finishRead(ticket uint64) {
	c.mu.Lock()
	delete(c.reads, ticket)
	c.tryDrain()
	c.mu.Unlock()
}

// earliestOutstanding returns the smallest ticket across all outstanding
// operations (in-flight ops and reads); empty=true when there are none. Caller
// holds c.mu.
func (c *Checker) earliestOutstanding() (uint64, bool) {
	var min uint64
	found := false
	consider := func(t uint64) {
		if !found || t < min {
			min = t
			found = true
		}
	}

	for ticket := range c.inflight {
		consider(ticket)
	}
	for ticket := range c.reads {
		consider(ticket)
	}

	return min, !found
}

// runProcessor is the response handler loop. Failures validate immediately
// against the candidate states; successes buffer by transaction id and drain in
// order (tryDrain).
func (c *Checker) runProcessor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case obs, ok := <-c.incoming:
			if !ok {
				return
			}
			c.handleObservation(obs)
		}
	}
}

func (c *Checker) handleObservation(obs observation) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.removeInflight(obs.ticket)

	// A transient error leaves the model untouched — the operation effectively
	// didn't happen (an ambiguous commit replays via the idempotency key, so a
	// transient that survives the client's retries truly didn't commit).
	if obs.err != nil && isTransient(obs.err) {
		dbg("TRANSIENT SKIP: ledger=%s op=%s err=%v", c.ledger, renderOp(obs.op), obs.err)
		return
	}

	if obs.err != nil {
		// A definitive failure consumes no transaction id. Accept it iff some
		// serialization of the in-flight operations reproduces the observed error.
		dbg("OP ERR: ledger=%s op=%s err=%v", c.ledger, renderOp(obs.op), obs.err)
		c.validateFailure(obs.observeTicket, obs.op, obs.err)
		return
	}

	c.insertPending(&pendingObservation{seq: obs.data.GetID(), obs: obs})
	c.tryDrain()
}

// tryDrain drains buffered successes in transaction-id order while safe: the head
// drains only once every outstanding operation (in-flight op or read) has a
// ticket greater than the head's observeTicket — i.e. was dispatched after the
// head was observed, so a later commit can't precede it and a read saw it. That
// gate lets failures and reads validate against the model with no skip. Caller
// holds c.mu.
func (c *Checker) tryDrain() {
	for len(c.pending) > 0 {
		head := c.pending[0]
		minTicket, empty := c.earliestOutstanding()
		if !empty && minTicket <= head.obs.observeTicket {
			return
		}

		c.pending = c.pending[1:]
		c.validateCommit(head.obs.op, head.obs.data)
	}
}

// insertPending inserts into c.pending, kept sorted ascending by transaction id.
// Caller holds c.mu.
func (c *Checker) insertPending(entry *pendingObservation) {
	i := 0
	for i < len(c.pending) && c.pending[i].seq.Cmp(entry.seq) < 0 {
		i++
	}

	c.pending = append(c.pending, nil)
	copy(c.pending[i+1:], c.pending[i:])
	c.pending[i] = entry
}
