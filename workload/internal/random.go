package internal

import (
	"fmt"
	"math/big"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

const USER_ACCOUNT_COUNT uint64 = 32

func RandomBigInt() *big.Int {
	v := random.GetRandom()
	ret := big.NewInt(0)
	ret.SetString(fmt.Sprintf("%d", v), 10)
	return ret
}

func GetRandomAddress() string {
	return random.RandomChoice([]string{"world", fmt.Sprintf("users:%d", random.GetRandom()%USER_ACCOUNT_COUNT)})
}

func RandomPostings() []shared.V2Posting {
	postings := []shared.V2Posting{}

	for range random.GetRandom()%2 + 1 {
		source := GetRandomAddress()
		destination := GetRandomAddress()
		amount := RandomBigInt()
		asset := random.RandomChoice([]string{"USD/2", "EUR/2", "COIN"})

		postings = append(postings, shared.V2Posting{
			Amount:      amount,
			Asset:       asset,
			Destination: destination,
			Source:      source,
		})
	}

	return postings
}

func RandomTimestamp(presentTime time.Time) *time.Time {
	offsetTime := presentTime.Add(time.Duration(-int64(random.GetRandom() % 10)))
	return random.RandomChoice([]*time.Time{
		nil,
		&offsetTime,
	})
}
