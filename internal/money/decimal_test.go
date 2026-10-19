package money

import "testing"

func TestParseAndFormat(t *testing.T) {
	for _, input := range []string{"0", "0.01", "1.2", "1000000.00", "999999999.99"} {
		cents, ok := Parse(input)
		if !ok {
			t.Fatalf("Parse(%q) failed", input)
		}
		if got, ok := Parse(Format(cents)); !ok || got != cents {
			t.Fatalf("round trip %q: %d, %v", input, got, ok)
		}
	}
	for _, input := range []string{"", "-1", "+1", "1.", ".1", "1.234", "NaN", "1e2", "1.2.3", "1000000000", " 1", "1.a"} {
		if _, ok := Parse(input); ok {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestFee(t *testing.T) {
	for _, tc := range []struct{ amount, rate, fixed, want string }{
		{"100.00", "2.50", "1.00", "3.50"},
		{"0.20", "2.50", "0.00", "0.01"},
		{"0.19", "2.50", "0.00", "0.00"},
		{"50.00", "0.00", "1.00", "1.00"},
		{"1000000.00", "100.00", "9999.99", "1009999.99"},
	} {
		if got, ok := Fee(tc.amount, tc.rate, tc.fixed); !ok || got != tc.want {
			t.Errorf("Fee(%+v) = %q, %v", tc, got, ok)
		}
	}
	for _, tc := range [][3]string{{"1", "100.01", "0"}, {"1", "0", "10000"}, {"1", "-1", "0"}, {"bad", "0", "0"}} {
		if _, ok := Fee(tc[0], tc[1], tc[2]); ok {
			t.Errorf("accepted invalid fee inputs: %v", tc)
		}
	}
}
