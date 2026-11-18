package cli

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeIntegralNumbersPreservesNestedValues(t *testing.T) {
	var document genericResource
	decoder := json.NewDecoder(strings.NewReader(`{"nested":[3.0,{"limit":9007199254740993.0,"fraction":3.5,"tiny":1e-999999999,"huge":1e999999999}],"text":"3.0"}`))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&document))
	encoded, err := json.Marshal(normalizeIntegralJSONNumbers(document))
	require.NoError(t, err)
	require.Equal(t, `{"nested":[3,{"fraction":3.5,"huge":1e999999999,"limit":9007199254740993,"tiny":1e-999999999}],"text":"3.0"}`, string(encoded))
}

func TestNormalizeIntegralNumbersMatchesExactOracle(t *testing.T) {
	for _, mantissa := range []string{
		"0", "-0.000", "0.00012300", "-12.3400", "1000.000",
		"9007199254740993", "9223372036854775807", "-9223372036854775808",
		"9223372036854775808", "-9223372036854775809",
	} {
		for exponent := -24; exponent <= 24; exponent++ {
			number := json.Number(fmt.Sprintf("%se%d", mantissa, exponent))
			exact, ok := new(big.Rat).SetString(string(number))
			require.True(t, ok)
			want := number
			if exact.IsInt() && exact.Num().IsInt64() {
				want = json.Number(exact.Num().String())
			}
			require.Equal(t, want, normalizeIntegralJSONNumber(number), "input %s", number)
		}
	}
}
