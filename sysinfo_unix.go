//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
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

// getGPUTelemetry queries nvidia-smi for NVIDIA GPU information and thermals
func getGPUTelemetry() string {
	smiPath, err := exec.LookPath("nvidia-smi")
	if err != nil {
		if _, statErr := os.Stat("/usr/lib/wsl/lib/nvidia-smi"); statErr == nil {
			smiPath = "/usr/lib/wsl/lib/nvidia-smi"
		} else {
			return "No dedicated GPU detected"
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, smiPath, "--query-gpu=name,memory.total,memory.used,temperature.gpu,utilization.gpu", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return "GPU telemetry unavailable"
	}

	return parseNvidiaSmiOutput(string(out))
}
