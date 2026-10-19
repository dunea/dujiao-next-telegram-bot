// Package money provides bounded, nonnegative two-decimal arithmetic without floats.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

func Parse(value string) (int64, bool) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 9 {
		return 0, false
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, false
		}
		fraction = (parts[1] + "0")[:2]
	}
	digits := parts[0] + fraction
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	cents, err := strconv.ParseInt(digits, 10, 64)
	return cents, err == nil
}

func Format(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// Fee matches Dujiao-Next: round(base * percent / 100 + fixed, 2).
func Fee(amount, rate, fixed string) (string, bool) {
	base, amountOK := Parse(amount)
	basisPoints, rateOK := Parse(rate)
	fixedCents, fixedOK := Parse(fixed)
	if !amountOK || !rateOK || !fixedOK || basisPoints > 10000 || fixedCents >= 1000000 {
		return "", false
	}
	return Format((base*basisPoints+5000)/10000 + fixedCents), true
}
