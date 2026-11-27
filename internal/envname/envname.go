// Package envname reads a variable under its new CLOUDFIELD_ name, else
// under its AETHER_ name.
//
// A name set to the empty string counts as unset, as every reader of an
// AETHER_ variable already treats it. When both names are set, the
// CLOUDFIELD_ value wins without a warning.
package envname

import (
	"os"
	"strings"
)

const oldPrefix = "AETHER_"

// New is the CLOUDFIELD_ name of old, an AETHER_ name.
func New(old string) string {
	return "CLOUDFIELD_" + strings.TrimPrefix(old, oldPrefix)
}

// Get reads old through getenv: the value of New(old) when it is not empty,
// else the value of old.
func Get(getenv func(string) string, old string) string {
	if v := getenv(New(old)); v != "" {
		return v
	}
	return getenv(old)
}

// Getenv is Get through os.Getenv. It has os.Getenv's signature, so a reader
// that takes a getenv function can be handed it.
func Getenv(old string) string {
	return Get(os.Getenv, old)
}

// Legacy reads old under its AETHER_ name alone, as an image older than
// v0.8.7, which knows no CLOUDFIELD_ names, reads it: for checking what a
// rollback to such an image would do.
func Legacy(old string) string {
	return os.Getenv(old)
}
