//go:build !unix

package serversetup

import "errors"

func lockConfig(string) (func(), error) {
	return nil, errors.New("serversetup: the config lock needs a Unix system")
}
