package utils

import (
	"math"

	tdb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
)

var PrecisionMap = map[string]int{
	"sol":  6,
	"btc":  8,
	"bnb":  5,
	"trx":  4,
	"eth":  6,
	"dash": 5,
	"xrp":  5,
	"ton":  4,
	"usdt": 2,
	"usd":  2,
	"ngn":  2,
	"usdc": 2,
}

func ApproximateAmount(currency string, amount float64) float64 {
	if amount < 0 {
		panic("amount cannot be less than 0")
	}
	exp := math.Pow10(PrecisionMap[currency])
	tmp := amount * exp
	if math.Floor(tmp-0.5) != math.Floor(tmp) {
		tmp += 0.5
	}
	return math.Floor(tmp) / exp
}

func ToAmount(val float64) tdb_types.Uint128 {
	return tdb_types.ToUint128(uint64(math.Floor(val * 1e9)))
}

func FromAmount(amount tdb_types.Uint128) float64 {
	val := amount.BigInt()
	return float64(val.Uint64()) * 1e-9
}
