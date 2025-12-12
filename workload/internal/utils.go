package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/assert"
	"github.com/antithesishq/antithesis-sdk-go/random"
	client "github.com/formancehq/formance-sdk-go/v3"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/operations"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/sdkerrors"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
	"github.com/formancehq/formance-sdk-go/v3/pkg/retry"
)

type Details map[string]any

func (d *Details) With(extra Details) Details {
	out := make(map[string]any)
	for k, v := range *d {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

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
	details := Details{
		"ledger": name,
	}
	res, err := client.Ledger.V2.CreateLedger(ctx, operations.V2CreateLedgerRequest{
		Ledger: name,
		V2CreateLedgerRequest: shared.V2CreateLedgerRequest{
			Bucket: &bucket,
		},
	})
	assert.Sometimes(err == nil, "should be able to create ledger", details.With(Details{
		"error": err,
	}))
	if err != nil {
		return nil, err
	}
	_, err = client.Ledger.V2.GetLedger(ctx, operations.V2GetLedgerRequest{
		Ledger: name,
	})
	var getTxError *sdkerrors.V2ErrorResponse
	if errors.As(err, &getTxError) {
		assert.AlwaysOrUnreachable(getTxError.ErrorCode != shared.V2ErrorsEnumNotFound, "should always be able to get created ledger", details)
	}
	return res, nil
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

	randomIndex := random.GetRandom() % uint64(len(ledgers))

	return ledgers[randomIndex], nil
}

func GetPresentTime(ctx context.Context, client *client.Formance, ledger string) (*time.Time, error) {
	res, err := client.Ledger.V2.ListTransactions(ctx, operations.V2ListTransactionsRequest{
		Ledger: ledger,
	})
	assert.Sometimes(err == nil, "should be able to get the latest transaction", Details{
		"ledger": ledger,
	})
	if err != nil {
		return nil, err
	}
	if len(res.V2TransactionsCursorResponse.Cursor.Data) == 0 {
		now := time.Now()
		return &now, err
	} else {
		return &res.V2TransactionsCursorResponse.Cursor.Data[0].Timestamp, nil
	}
}

func SuccessOrInsufficientFunds(err error) bool {
	var sdkError *sdkerrors.V2ErrorResponse
	if errors.As(err, &sdkError) {
		return sdkError.ErrorCode == shared.V2ErrorsEnumInsufficientFund
	}
	return true
}
