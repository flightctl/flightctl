package crypto

import (
	"crypto/rand"
	"math"
	"math/big"
)

// returns a uniform random value in [1,max)
func randomSerial() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).SetInt64(math.MaxInt64-1))
	if err != nil {
		panic(err)
	}
	return new(big.Int).Add(serial, big.NewInt(1))
}
