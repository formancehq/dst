package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/dst/workload/internal"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/go-libs/v3/pointer"
	"github.com/formancehq/go-libs/v3/publish"

	"github.com/confluentinc/confluent-kafka-go/kafka"

	"github.com/formancehq/ledger/pkg/events"
)

func CheckTransactions(ctx context.Context, client *client.Formance, ledgers []string) {
	log.Printf("composer: checking events")

	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers":  "formance-kafka-bootstrap.kafka.svc.cluster.local:9092",
		"group.id":           fmt.Sprintf("correctness-%v", random.GetRandom()),
		"auto.offset.reset":  "smallest",
		"enable.auto.commit": false,
	})
	assert.Always(err == nil, "should create kafka consumer successfully", internal.Details{
		"err": err,
	})
	if err != nil {
		return
	}

	err = consumer.SubscribeTopics([]string{"stack0.ledger"}, nil)
	assert.Always(err == nil, "should find ledger topic", internal.Details{
		"err": err,
	})
	if err != nil {
		return
	}

	checkForAllLedgers(ctx, client, consumer, ledgers)

	consumer.Close() //nolint:errcheck
}

type EventChecker struct {
	data       []shared.V2Transaction
	nextCursor *string
	// we store a few extra events since reorders happen
	eventBuffer []events.CommittedTransactions
}

func (checker *EventChecker) fetchNext(ctx context.Context, client *client.Formance, ledger string, details internal.Details) error {
	if len(checker.data) == 0 {
		if checker.nextCursor != nil {
			res, err := client.Ledger.V2.ListTransactions(ctx, operations.V2ListTransactionsRequest{
				Ledger: ledger,
				Cursor: checker.nextCursor,
			})
			assert.Always(
				err == nil,
				"can list transactions",
				details.With(internal.Details{
					"error": err,
				}),
			)
			if err != nil {
				return err
			}
			checker.data = res.V2TransactionsCursorResponse.Cursor.Data
			checker.nextCursor = res.V2TransactionsCursorResponse.Cursor.Next
		}
	}
	return nil
}

func (checker *EventChecker) findInBuffer(details internal.Details) bool {
	found := false
	if len(checker.data) > 0 {
		if index := slices.IndexFunc(checker.eventBuffer, func(bufTx events.CommittedTransactions) bool {
			return *bufTx.Transactions[0].ID == checker.data[0].ID.Uint64()
		}); index != -1 {
			eventTx := checker.eventBuffer[index]
			assert.Always(transactionEventMatches(checker.data[0], eventTx), "event transaction should match", details.With(internal.Details{
				"expected": checker.data[0],
				"actual":   eventTx,
			}))
			checker.eventBuffer = slices.Delete(checker.eventBuffer, index, index+1)
			checker.data = checker.data[1:]
			found = true
		}
	}
	return found
}

func checkForAllLedgers(ctx context.Context, client *client.Formance, consumer *kafka.Consumer, ledgers []string) {
	var (
		details  = internal.Details{}
		checkers = map[string]EventChecker{}
		err      error
	)
	for _, ledger := range ledgers {
		res, err := client.Ledger.V2.ListTransactions(ctx, operations.V2ListTransactionsRequest{
			Ledger: ledger,
			Sort:   pointer.For("id:asc"),
		})
		assert.Always(
			err == nil,
			"can list transactions",
			details.With(internal.Details{
				"error": err,
			}),
		)
		if err != nil {
			return
		}
		checkers[ledger] = EventChecker{
			data:        res.V2TransactionsCursorResponse.Cursor.Data,
			nextCursor:  res.V2TransactionsCursorResponse.Cursor.Next,
			eventBuffer: []events.CommittedTransactions{},
		}
	}

	assignment := []kafka.TopicPartition{}

	log.Printf("Consuming & matching events...")
events:
	for {
		ev := consumer.Poll(1000)
		switch e := ev.(type) {
		case *kafka.Message:
			var msg publish.EventMessage
			dec := json.NewDecoder(bytes.NewReader(e.Value))
			dec.UseNumber()
			err := dec.Decode(&msg)
			if err != nil {
				panic(err)
			}
			if msg.Type != "COMMITTED_TRANSACTIONS" {
				continue
			}
			eventTx, err := internal.ExtractEventPayload(msg.Payload, func(tx events.CommittedTransactions) error {
				if len(tx.Transactions) != 1 || tx.Transactions[0].ID == nil {
					return errors.New("malformed message")
				}
				return nil
			})
			if err != nil {
				assert.Unreachable("events should not be malformed", details.With(internal.Details{
					"error":   err,
					"payload": msg.Payload,
				}))
				return
			}

			checker, ok := checkers[eventTx.Ledger]
			if !ok {
				assert.Unreachable("events should target an existing ledger", details)
				return
			}
			if len(checker.data) > 0 && *eventTx.Transactions[0].ID == checker.data[0].ID.Uint64() {
				assert.Always(transactionEventMatches(checker.data[0], *eventTx), "event transaction should match", details.With(internal.Details{
					"expected": checker.data[0],
					"actual":   eventTx,
				}))
				checker.data = checker.data[1:]
				looping := true
				for looping {
					err := checker.fetchNext(ctx, client, eventTx.Ledger, details)
					if err != nil {
						return
					}
					looping = checker.findInBuffer(details)
				}
			} else if len(checker.eventBuffer) > 16 {
				assert.Unreachable("events ordering should be similar", details)
				return
			} else {
				checker.eventBuffer = append(checker.eventBuffer, *eventTx)
			}
			checkers[eventTx.Ledger] = checker
		case kafka.Error:
			if !strings.Contains(strings.ToLower(e.Error()), "unknown topic or partition") {
				assert.Unreachable("should not receive kafka error", details.With(internal.Details{
					"error": e,
				}))
				return
			}
		case nil:
			// try to get assignment if not fetched yet
			if len(assignment) == 0 {
				assignment, err = consumer.Assignment()
				if err != nil {
					panic(err)
				}
				time.Sleep(time.Second)
				continue
			}
			pos, err := consumer.Position(assignment)
			if err != nil {
				panic(err)
			}
			_, high, err := consumer.GetWatermarkOffsets("stack0.ledger", assignment[0].Partition)
			if err != nil {
				panic(err)
			}
			if int64(pos[0].Offset) >= high {
				break events
			}
		default:
			fmt.Printf("Ignored kafka event: %v\n", e)
		}
	}

	log.Printf("Checking for remaining unmatched events...")
	for ledger, checker := range checkers {
		allReceived := len(checker.data) == 0 && checker.nextCursor == nil
		if !allReceived {
			assert.Unreachable("all transactions should have a corresponding event", details.With(internal.Details{
				"ledger":                 ledger,
				"remaining_transactions": checker.data,
				"next_cursor":            checker.nextCursor,
			}))
			return
		}

		if len(checker.eventBuffer) > 0 {
			assert.Unreachable("all events should have a corresponding transaction", details.With(internal.Details{
				"ledger":           ledger,
				"unmatched_events": checker.eventBuffer,
			}))
			fmt.Printf("%v\n", details.With(internal.Details{
				"ledger":           ledger,
				"unmatched_events": checker.eventBuffer,
			}))
			return
		}
	}
	log.Printf("Received all events")
}

func transactionEventMatches(actual shared.V2Transaction, event events.CommittedTransactions) bool {
	if *actual.InsertedAt != event.Transactions[0].InsertedAt.Time {
		return false
	}
	if len(actual.Postings) != len(event.Transactions[0].Postings) {
		return false
	}
	for idx, posting := range actual.Postings {
		if posting.Amount.Cmp(event.Transactions[0].Postings[idx].Amount) != 0 {
			return false
		}
		if posting.Asset != event.Transactions[0].Postings[idx].Asset || posting.Source != event.Transactions[0].Postings[idx].Source || posting.Destination != event.Transactions[0].Postings[idx].Destination {
			return false
		}
	}
	return true
}
