// Package shuffle randomizes the order of call targets, so concurrent
// callers spread over them instead of all trying the first.
package shuffle

import (
	"crypto/rand"
	"math/big"
)

// Strings randomizes the order of s in place (Fisher-Yates). The draws come
// from crypto/rand: math/rand is out of bounds (gosec G404).
func Strings(s []string) {
	for i := len(s) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			// crypto/rand.Read succeeds or panics (Go 1.24+); an order
			// left as it is remains a valid order.
			return
		}

		k := int(j.Int64())
		s[i], s[k] = s[k], s[i]
	}
}
