//go:build unix

package materialize

import (
	"os"
	"syscall"
)

// ownerUID extracts the file owner's UID on Unix. Returns false on systems
// that don't expose syscall.Stat_t through FileInfo.Sys().
func ownerUID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return stat.Uid, true
}
