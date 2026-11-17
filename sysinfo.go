package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func handleSysInfo(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	// 1. OS Uptime
	uptimeStr := "Unknown"
	if upData, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(upData))
		if len(parts) > 0 {
			if secs, err := strconv.ParseFloat(parts[0], 64); err == nil {
				uptimeStr = (time.Duration(secs) * time.Second).String()
			}
		}
	}

	// 2. Host Memory
	memTotal, memAvail := "Unknown", "Unknown"
	if memData, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(memData), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				memTotal = parseMemLine(line)
			}
			if strings.HasPrefix(line, "MemAvailable:") {
				memAvail = parseMemLine(line)
			}
		}
	}

	// 3. CPU Load Averages (Cleaned up)
	loadAvg := "Unknown"
	if loadData, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(loadData))
		if len(parts) >= 3 {
			loadAvg = fmt.Sprintf("%s, %s, %s", parts[0], parts[1], parts[2])
		}
	}

	// 4. Bot Process Memory & Goroutines
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	botMem := fmt.Sprintf("%.2f MB", float64(memStats.Alloc)/1024/1024)
	goroutines := fmt.Sprintf("%d", runtime.NumGoroutine())

	// Build the Embed
	embed := &discordgo.MessageEmbed{
		Title: "🖥️ Host System Information",
		Color: 0xFF6B35,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "🐧 Operating System", Value: getOSVersion(), Inline: true},
			{Name: "⏱️ OS Uptime", Value: uptimeStr, Inline: true},
			{Name: "⚙️ CPU Info", Value: getCPUInfo(), Inline: true},
			{Name: "🧠 Host Memory", Value: fmt.Sprintf("Total: %s\nAvail: %s", memTotal, memAvail), Inline: true},
			{Name: "🎮 Dedicated GPU", Value: getGPUTelemetry(), Inline: true},
			{Name: "💾 Disk Space (Root)", Value: getDiskInfo(), Inline: true},
			{Name: "📈 CPU Load (1, 5, 15m)", Value: loadAvg, Inline: true},
			{Name: "🤖 Bot Process", Value: fmt.Sprintf("RAM: %s\nGoroutines: %s", botMem, goroutines), Inline: true},
			{Name: "🐹 Go Version", Value: runtime.Version(), Inline: true},
			{Name: "🏛️ Architecture", Value: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH), Inline: true},
		},
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
	})
}

// parseMemLine converts kilobytes from /proc/meminfo into GB
func parseMemLine(line string) string {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		if kb, err := strconv.ParseFloat(parts[1], 64); err == nil {
			return fmt.Sprintf("%.2f GB", kb/1024/1024)
		}
	}
	return "Unknown"
}

// getOSVersion parses /etc/os-release to find the pretty name (e.g., Ubuntu 24.04 LTS)
func getOSVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"'`)
		}
	}
	return runtime.GOOS
}

// getCPUInfo counts processors and grabs the model name from /proc/cpuinfo
func getCPUInfo() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "Unknown CPU"
	}
	model := "Unknown CPU"
	cores := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") && model == "Unknown CPU" {
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				model = strings.TrimSpace(parts[1])
			}
		}
		if strings.HasPrefix(line, "processor") {
			cores++
		}
	}
	
	// Truncate CPU model if it's absurdly long so it fits neatly in the embed
	if len(model) > 30 {
		model = strings.ReplaceAll(model, "(R)", "")
		model = strings.ReplaceAll(model, "(TM)", "")
		model = strings.ReplaceAll(model, "CPU", "")
		model = strings.TrimSpace(model)
	}
	
	return fmt.Sprintf("%s\n**%d Cores**", model, cores)
}

// parseNvidiaSmiOutput parses comma-separated output from nvidia-smi:
// name, memory.total, memory.used, temperature.gpu, utilization.gpu
func parseNvidiaSmiOutput(raw string) string {
	line := strings.TrimSpace(raw)
	if line == "" {
		return "No dedicated GPU detected"
	}
	if idx := strings.Index(line, "\n"); idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}
	parts := strings.Split(line, ",")
	if len(parts) < 5 {
		return "GPU telemetry unavailable"
	}
	name := strings.TrimSpace(parts[0])
	total, errTotal := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	used, errUsed := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	temp := strings.TrimSpace(parts[3])
	util := strings.TrimSpace(parts[4])

	if errTotal != nil || errUsed != nil {
		return fmt.Sprintf("**%s**\nTemp: %s°C • Load: %s%%", name, temp, util)
	}

	pct := 0.0
	if total > 0 {
		pct = (used / total) * 100
	}

	return fmt.Sprintf("**%s**\nVRAM: %.0f / %.0f MB (%.1f%%)\nTemp: %s°C • Load: %s%%", name, used, total, pct, temp, util)
}
