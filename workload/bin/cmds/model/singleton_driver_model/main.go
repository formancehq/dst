package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/retry"
)

func main() {
	log.Println("composer: singleton_driver_model")

	ctx := context.Background()

	// Self-terminate after MODEL_MAX_SECONDS so an orphaned driver can't keep
	// hammering a shared ledger into the next run.
	if secs := os.Getenv("MODEL_MAX_SECONDS"); secs != "" {
		if d, err := strconv.Atoi(secs); err == nil && d > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Second)
			defer cancel()
		}
	}

	numLedgers := envInt("MODEL_LEDGERS", defaultLedgers)
	numWorkers := envInt("MODEL_WORKERS", defaultWorkers)

	cl := newClient()

	// Unique per-run prefix so a fresh invocation never reattaches to a previous
	// run's ledgers (the model starts empty; inherited committed state would
	// diverge).
	runID := fmt.Sprintf("%016x", random.GetRandom())

	checkers := make([]*Checker, 0, numLedgers)
	for i := 0; i < numLedgers; i++ {
		name := fmt.Sprintf("model-%s-%d", runID, i)
		if !setupLedger(ctx, cl, name) {
			return
		}
		checkers = append(checkers, NewChecker(name))
	}

	var processors sync.WaitGroup
	for _, c := range checkers {
		processors.Add(1)
		go func(c *Checker) {
			defer processors.Done()
			c.runProcessor(ctx)
		}(c)
	}

	log.Printf("starting %d workers across %d ledgers", numWorkers, numLedgers)

	var workers sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			runWorker(ctx, cl, checkers)
		}()
	}

	// Workers stop on ctx.Done. Close each processor's channel so it can exit.
	workers.Wait()
	for _, c := range checkers {
		close(c.incoming)
	}
	processors.Wait()
}

// runWorker loops until ctx is done. Each iteration picks a random ledger, then
// either reads it (1-in-5) or dispatches a generated operation, pushing the
// observation to that ledger's processor.
func runWorker(ctx context.Context, cl *client.Formance, checkers []*Checker) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c := random.RandomChoice(checkers)

		switch random.RandomChoice([]uint8{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		case 0, 1: // volume read
			runRead(ctx, cl, c)
			time.Sleep(workerLoopPause)
			continue
		case 2, 3: // metadata write
			runMetaWrite(ctx, cl, c)
			time.Sleep(workerLoopPause)
			continue
		case 4: // metadata read
			runMetaRead(ctx, cl, c)
			time.Sleep(workerLoopPause)
			continue
		case 5: // transaction read
			runTransactionRead(ctx, cl, c)
			time.Sleep(workerLoopPause)
			continue
		}

		// Volume operation (create/revert). Generate under the lock: a revert reads
		// the committed state to pick a target, and the ticket must be reserved in
		// dispatch order.
		c.mu.Lock()
		op := generateOperation(c.modelState)
		ticket := c.registerInflight(op)
		metaRefs := c.registerCreateAccountMeta(op, ticket)
		c.mu.Unlock()

		data, err := sendOperation(ctx, cl, c.ledger, op)

		// Snapshot the ticket high-water at observe (atomic, lock-free); the drain
		// gate compares outstanding tickets against it.
		observeTicket := c.ticketSeq.Load()

		// Settle any metadata the create carried against its outcome, on the
		// register track (independent of the volume re-order buffer below).
		c.mu.Lock()
		c.settleCreateMeta(op, metaRefs, data, err == nil, ticket, observeTicket)
		c.mu.Unlock()

		obs := observation{
			ticket:        ticket,
			op:            op,
			data:          data,
			err:           err,
			observeTicket: observeTicket,
		}

		select {
		case <-ctx.Done():
			return
		case c.incoming <- obs:
		}

		time.Sleep(workerLoopPause)
	}
}

// runMetaWrite dispatches one metadata set/delete and records the outcome. A
// delete that returns NOT_FOUND is treated as committed (the key was already
// absent); other errors are treated as not having happened.
func runMetaWrite(ctx context.Context, cl *client.Formance, c *Checker) {
	c.mu.Lock()
	op := generateMetaWrite(c)
	c.mu.Unlock()

	err := sendMetaOp(ctx, cl, c.ledger, op)

	c.mu.Lock()
	switch {
	case err == nil, op.write.deleted && isNotFound(err):
		c.metaStore.commit(op.cell, op.write, c.ticketSeq.Load())
	default:
		c.metaStore.drop(op.cell, op.write)
	}
	c.mu.Unlock()
}

// setupLedger creates one ledger. With the unique per-run names a conflict cannot
// occur, so any non-transient error is a genuine setup failure: the model can't
// run against a missing ledger, so it asserts Unreachable. Shutdown / faults
// during setup return false to stop the run without a finding.
func setupLedger(ctx context.Context, cl *client.Formance, name string) bool {
	_, err := internal.CreateLedger(ctx, cl, name, name)
	if err == nil {
		return true
	}

	if isTransient(err) {
		return false
	}

	assert.Unreachable("singleton_driver_model: ledger setup failed", internal.Details{
		"ledger": name,
		"error":  err.Error(),
	})

	return false
}

// newClient builds a ledger client with a long retry window so an ambiguous
// commit (response lost mid-fault) replays to the committed result via the
// operation's idempotency key instead of being dropped as "didn't happen".
func newClient() *client.Formance {
	gateway := os.Getenv("GATEWAY_URL")
	if gateway == "" {
		gateway = "http://gateway.stack0.svc.cluster.local:8080/"
	}

	return client.New(
		client.WithServerURL(gateway),
		client.WithClient(&http.Client{Timeout: time.Minute}),
		client.WithRetryConfig(retry.Config{
			Strategy: "backoff",
			Backoff: &retry.BackoffStrategy{
				InitialInterval: 200,
				Exponent:        1.5,
				MaxElapsedTime:  300_000,
			},
			RetryConnectionErrors: true,
		}),
	)
}

// envInt reads an int from env, defaulting on missing or invalid.
func envInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}

	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		log.Printf("warning: invalid %s=%q, using default %d", key, raw, def)
		return def
	}

	return v
}
