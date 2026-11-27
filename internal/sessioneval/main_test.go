package sessioneval

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if kind := os.Getenv(helperEnv); kind != "" {
		os.Exit(Helper(kind, os.Args[1:]))
	}
	os.Exit(m.Run())
}
