//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkInherited refuses an inherited file that someone other than the user
// could have written: it can declare agents, that is commands a run executes,
// and the user never named it. The file must belong to the user or to root,
// and be writable by its owner only.
func checkInherited(st os.FileInfo) error {
	if perm := st.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("writable by others (%04o): an inherited configuration runs its agents, make it writable by its owner only or add root: true below it", perm)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != 0 && int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("owned by uid %d, not by you or root: an inherited configuration runs its agents, add root: true below it to stop inheriting it", sys.Uid)
	}
	return nil
}
