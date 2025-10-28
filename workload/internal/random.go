package internal

import (
	"fmt"
	"math/big"
	"time"

	"github.com/antithesishq/antithesis-sdk-go/random"
	"github.com/formancehq/formance-sdk-go/v3/pkg/models/shared"
)

const USER_ACCOUNT_COUNT uint64 = 32;

// Generate a random big int while making sure we explore all orders of magnitude
func RandomBigInt() *big.Int {
	// first generate a random 128-bit int
	out := big.NewInt(0)
	out.SetString(fmt.Sprintf("%d", random.GetRandom()), 10)
	high := big.NewInt(0)
	high.SetString(fmt.Sprintf("%d", random.GetRandom()), 10)
	high.Lsh(high, 64)
	out.Add(out, high)
	// then shift it to a random order of magnitude
	out.Rsh(out, uint(random.GetRandom()%128))
	return out
}

func GetRandomAddress() string {
	return random.RandomChoice([]string{"world", fmt.Sprintf("users:%d", random.GetRandom()%USER_ACCOUNT_COUNT)})
}

func RandomPostings() []shared.V2Posting {
	postings := []shared.V2Posting{}

	for range random.GetRandom()%2 + 1 {
		postings = append(postings, shared.V2Posting{
			Amount:      RandomBigInt(),
			Asset:       RandomAsset(),
			Destination: GetRandomAddress(),
			Source:      GetRandomAddress(),
		})
	}

	return postings
}

func RandomMonetary() shared.V2Monetary {
	return shared.V2Monetary{
		Amount: RandomBigInt(),
		Asset:  RandomAsset(),
	}
}

func RandomAsset() string {
	return random.RandomChoice([]string{"USD/2", "EUR/2", "COIN"})
}

func RandomTimestamp(presentTime time.Time) *time.Time {
	offsetTime := presentTime.Add(time.Duration(-int64(random.GetRandom()%uint64(100*time.Second))))
	return random.RandomChoice([]*time.Time{
		nil,
		&offsetTime,
	})
}

