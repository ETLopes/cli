// Command dtx turns a video URL into practice tracks a Yamaha DTX-PRO drum
// module can play.
package main

import (
	"os"

	"github.com/eduardolopes/dtx/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
