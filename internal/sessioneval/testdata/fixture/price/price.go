// Package price computes order amounts in cents.
package price

// Discount returns total after taking percent off.
func Discount(total, percent int) int {
	return total * percent / 100
}
