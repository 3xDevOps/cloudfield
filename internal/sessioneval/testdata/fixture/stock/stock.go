// Package stock tracks how many units of an item are left.
package stock

import "errors"

var ErrInsufficient = errors.New("stock: not enough units")

// Reserve takes n units out of available and returns what is left.
func Reserve(available, n int) (int, error) {
	if n < 0 || n > available {
		return available, ErrInsufficient
	}
	return available - n, nil
}
