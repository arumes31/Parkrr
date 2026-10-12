package handlers

import (
	"math"
	"testing"
)

func TestValidCategoryPrices(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		monthly  float64
		yearly   float64
		expected bool
	}{
		{name: "free tariff", expected: true},
		{name: "ordinary tariff", monthly: 85, yearly: 850, expected: true},
		{name: "maximum", monthly: maxMoneyAmount, yearly: maxMoneyAmount, expected: true},
		{name: "negative monthly", monthly: -0.01},
		{name: "negative yearly", yearly: -0.01},
		{name: "monthly above maximum", monthly: maxMoneyAmount + 1},
		{name: "yearly above maximum", yearly: maxMoneyAmount + 1},
		{name: "not a number", monthly: math.NaN()},
		{name: "infinite", yearly: math.Inf(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validCategoryPrices(&categoryRequest{
				DefaultMonthlyCost: tc.monthly,
				DefaultYearlyCost:  tc.yearly,
			}); got != tc.expected {
				t.Errorf("validCategoryPrices = %v, want %v", got, tc.expected)
			}
		})
	}
}
