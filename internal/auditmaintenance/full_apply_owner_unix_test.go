//go:build unix

package auditmaintenance_test

import (
	"os"
	"syscall"
)

func sameFullCompactOwner(first os.FileInfo, second os.FileInfo) bool {
	firstStat, firstOK := first.Sys().(*syscall.Stat_t)
	secondStat, secondOK := second.Sys().(*syscall.Stat_t)
	return firstOK && secondOK && firstStat.Uid == secondStat.Uid && firstStat.Gid == secondStat.Gid
}
