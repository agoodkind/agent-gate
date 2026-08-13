//go:build darwin

package auditstorage

import (
	"strconv"
	"syscall"
)

func formatCutoverDevice(stat *syscall.Stat_t) string {
	return strconv.FormatInt(int64(stat.Dev), 10)
}
