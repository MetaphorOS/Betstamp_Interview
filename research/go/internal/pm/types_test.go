package pm

import (
	"math"
	"testing"
)

func TestMarketSidesAndComplement(t *testing.T) {
	m := Market{
		Outcomes:      `["Lakers", "Jazz"]`,
		OutcomePrices: `["0.585", "0.415"]`,
		ClobTokenIDs:  `["token-a", "token-b"]`,
		BestBid:       ptr(0.58),
		BestAsk:       ptr(0.59),
	}
	sides, err := m.Sides()
	if err != nil {
		t.Fatal(err)
	}
	if len(sides) != 2 || sides[0].Token != "token-a" || sides[1].Price != 0.415 {
		t.Fatalf("sides = %+v", sides)
	}
	if got, ok := m.Mid(0); !ok || got != 0.585 {
		t.Fatalf("mid0 = %v %v", got, ok)
	}
	if got, ok := m.Mid(1); !ok || math.Abs(got-0.415) > 1e-9 {
		t.Fatalf("mid1 = %v %v", got, ok)
	}
}

func TestDecimalToAmerican(t *testing.T) {
	cases := []struct {
		p    float64
		want int
	}{
		{0.5, 100},
		{0.585, -141},
		{0.415, 141},
	}
	for _, c := range cases {
		got, ok := DecimalToAmerican(c.p)
		if !ok || got != c.want {
			t.Errorf("p=%v got=%d want=%d", c.p, got, c.want)
		}
	}
}

func ptr(v float64) *float64 { return &v }
