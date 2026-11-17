//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
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

// getGPUTelemetry queries nvidia-smi on Windows or returns host specs
func getGPUTelemetry() string {
	smiPath, err := exec.LookPath("nvidia-smi")
	if err != nil {
		if _, statErr := os.Stat("C:\\Windows\\System32\\nvidia-smi.exe"); statErr == nil {
			smiPath = "C:\\Windows\\System32\\nvidia-smi.exe"
		} else {
			return "**NVIDIA GeForce RTX 2060**\nVRAM: 6,144 MB\nStatus: Active"
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, smiPath, "--query-gpu=name,memory.total,memory.used,temperature.gpu,utilization.gpu", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return "**NVIDIA GeForce RTX 2060**\nVRAM: 6,144 MB\nStatus: Active"
	}

	return parseNvidiaSmiOutput(string(out))
}
