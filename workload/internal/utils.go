package internal

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/random"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/formance-sdk-go/v3/pkg/retry"
)

func NewClient() *client.Formance {
	gateway := os.Getenv("GATEWAY_URL")
	if gateway == "" {
		gateway = "http://gateway.stack0.svc.cluster.local:8080/"
	}
	return client.New(
		client.WithServerURL(gateway),
		client.WithClient(&http.Client{
			Timeout: time.Minute,
		}),
		client.WithRetryConfig(retry.Config{
			Strategy: "backoff",
			Backoff: &retry.BackoffStrategy{
				InitialInterval: 200,
				Exponent:        1.5,
				MaxElapsedTime:  10_000,
			},
			RetryConnectionErrors: true,
		}),
	)
}

func CreateLedger(ctx context.Context, client *client.Formance, name string, bucket string) (*operations.V2CreateLedgerResponse, error) {
	res, err := client.Ledger.V2.CreateLedger(ctx, operations.V2CreateLedgerRequest{
		Ledger: name,
		V2CreateLedgerRequest: shared.V2CreateLedgerRequest{
			Bucket: &bucket,
		},
	})

	return res, err
}

func ListLedgers(ctx context.Context, client *client.Formance) ([]string, error) {
	res, err := client.Ledger.V2.ListLedgers(ctx, operations.V2ListLedgersRequest{})
	if err != nil {
		return nil, err
	}

	ledgers := []string{}
	for _, ledger := range res.V2LedgerListResponse.Cursor.Data {
		ledgers = append(ledgers, ledger.Name)
	}

	return ledgers, nil
}

func GetRandomLedger(ctx context.Context, client *client.Formance) (string, error) {
	ledgers, err := ListLedgers(ctx, client)
	if err != nil {
		return "", fmt.Errorf("error listing ledgers: %v", err)
	}

	if len(ledgers) == 0 {
		return "", fmt.Errorf("no ledgers found")
	}

	randomIndex := random.GetRandom()%uint64(len(ledgers))

	return ledgers[randomIndex], nil
}

func GetPresentTime(ctx context.Context, client *client.Formance, ledger string) (*time.Time, error) {
	tx, err := client.Ledger.V2.GetTransaction(ctx, operations.V2GetTransactionRequest{
		Ledger: ledger,
	})
	if AssertSometimesErrNil(err, "should be able to get the latest transaction", Details{}) {
		return nil, err
	}
	return &tx.V2GetTransactionResponse.Data.Timestamp, nil
}
