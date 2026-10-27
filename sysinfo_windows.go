//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// getDiskInfo checks the disk space of the current system drive on Windows
func getDiskInfo() string {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes int64

	rootPath, err := syscall.UTF16PtrFromString("C:\\")
	if err != nil {
		return "Unknown"
	}

	r1, _, _ := procGetDiskFreeSpace.Call(
		uintptr(unsafe.Pointer(rootPath)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if r1 == 0 {
		return "Unknown"
	}

	total := float64(totalNumberOfBytes) / (1024 * 1024 * 1024)
	free := float64(freeBytesAvailable) / (1024 * 1024 * 1024)
	used := total - free
	var percent float64
	if total > 0 {
		percent = (used / total) * 100
	}

	return fmt.Sprintf("Total: %.2f GB\nFree: %.2f GB\n*(%.1f%% Used)*", total, free, percent)
}
