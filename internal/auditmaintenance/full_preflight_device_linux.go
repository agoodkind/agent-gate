//go:build linux

package auditmaintenance

import (
	"strconv"
	"syscall"
)

func formatFullCompactDevice(stat *syscall.Stat_t) string {
	return strconv.FormatUint(stat.Dev, 10)
}
