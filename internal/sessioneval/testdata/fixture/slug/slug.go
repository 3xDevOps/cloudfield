// Package slug turns a product title into a URL path segment.
package slug

import "strings"

// Make lowercases title and joins its words with dashes.
func Make(title string) string {
	return strings.Join(strings.Fields(strings.ToLower(title)), "-")
}
