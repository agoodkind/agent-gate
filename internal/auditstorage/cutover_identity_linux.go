//go:build linux

package auditstorage

import (
	"strconv"
	"syscall"
)

func formatCutoverDevice(stat *syscall.Stat_t) string {
	return strconv.FormatUint(stat.Dev, 10)
}
