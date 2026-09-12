//go:build unix

package process

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup starts the child in its own process group and terminates
// the whole group on cancellation, so agent subprocesses do not linger.
func setProcessGroup(cmd *exec.Cmd, killDelay time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		go func() {
			time.Sleep(killDelay)
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}()
		return nil
	}
}
