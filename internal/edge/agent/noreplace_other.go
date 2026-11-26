//go:build !linux

package edgeagent

import "errors"

// renameNoReplace is unsupported off Linux: publishPin links instead.
func renameNoReplace(string, string) error { return errors.ErrUnsupported }
