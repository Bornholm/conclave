//go:build !unix

package process

import (
	"os/exec"
	"time"
)

func setProcessGroup(cmd *exec.Cmd, killDelay time.Duration) {}
