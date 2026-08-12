//go:build darwin

package auditmaintenance

import (
	"strconv"
	"syscall"
)

func formatFullCompactDevice(stat *syscall.Stat_t) string {
	return strconv.FormatInt(int64(stat.Dev), 10)
}
