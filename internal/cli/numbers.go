package cli

import (
	"encoding/json"
	"strconv"
	"strings"
)

func normalizeIntegralJSONNumbers(value interface{}) interface{} {
	switch typed := value.(type) {
	case genericResource:
		for key, child := range typed {
			typed[key] = normalizeIntegralJSONNumbers(child)
		}
	case map[string]interface{}:
		for key, child := range typed {
			typed[key] = normalizeIntegralJSONNumbers(child)
		}
	case []interface{}:
		for index, child := range typed {
			typed[index] = normalizeIntegralJSONNumbers(child)
		}
	case json.Number:
		return normalizeIntegralJSONNumber(typed)
	}
	return value
}

func normalizeIntegralJSONNumber(number json.Number) json.Number {
	mantissa, exponent, hasExponent := strings.Cut(strings.ToLower(string(number)), "e")
	var power int64
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign, mantissa = "-", mantissa[1:]
	}
	if point := strings.IndexByte(mantissa, '.'); point >= 0 {
		power -= int64(len(mantissa) - point - 1)
		mantissa = mantissa[:point] + mantissa[point+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return json.Number("0")
	}
	if hasExponent {
		parsed, err := strconv.ParseInt(exponent, 10, 32)
		if err != nil {
			return number
		}
		power += parsed
	}
	digits := strings.TrimRight(mantissa, "0")
	power += int64(len(mantissa) - len(digits))
	if power < 0 || power > 19 || int64(len(digits))+power > 19 {
		return number
	}
	integer := sign + digits + strings.Repeat("0", int(power))
	if _, err := strconv.ParseInt(integer, 10, 64); err != nil {
		return number
	}
	return json.Number(integer)
}
