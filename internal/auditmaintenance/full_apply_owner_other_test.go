//go:build !unix

package auditmaintenance_test

import "os"

func sameFullCompactOwner(os.FileInfo, os.FileInfo) bool {
	return true
}
