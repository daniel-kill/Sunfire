//go:build !windows

package main

import (
	"fmt"
	"syscall"
)

// getDiskInfo uses syscall to check the root file system usage on unix
func getDiskInfo() string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return "Unknown"
	}

	total := float64(stat.Blocks) * float64(stat.Bsize) / (1024 * 1024 * 1024)
	free := float64(stat.Bavail) * float64(stat.Bsize) / (1024 * 1024 * 1024)
	used := total - free
	var percent float64
	if total > 0 {
		percent = (used / total) * 100
	}

	return fmt.Sprintf("Total: %.2f GB\nFree: %.2f GB\n*(%.1f%% Used)*", total, free, percent)
}
