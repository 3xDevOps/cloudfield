// Command agent serves the acpmock agent on stdio in place of a real ACP
// adapter. Its one argument names the fixture: claude (the default) or codex.
// ACP_MOCK_BALLAST_MIB makes it hold that many mebibytes, as a real agent's
// process holds memory.
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
)

var ballast []byte

func main() {
	name := "claude"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	fix, err := acpmock.Load(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if mib, _ := strconv.Atoi(os.Getenv("ACP_MOCK_BALLAST_MIB")); mib > 0 {
		ballast = make([]byte, mib<<20)
		for i := 0; i < len(ballast); i += 4096 {
			ballast[i] = 1
		}
	}
	acpmock.New(fix).Serve(os.Stdin, os.Stdout)
}
