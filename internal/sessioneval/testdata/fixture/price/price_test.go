package price

import "testing"

func TestDiscount(t *testing.T) {
	if got := Discount(200, 25); got != 150 {
		t.Fatalf("Discount(200, 25) = %d, want 150", got)
	}
}
