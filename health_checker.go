// health_checker.go — background goroutine that periodically checks configured
// URLs and posts outages/recoveries to the Discord logging channel (#logs).
//
// Enabled via config:  "healthCheck": { "enabled": true, "intervalMinutes": 10,
//
//	"targets": ["https://...", ...] }
//
// The checker keeps per-target state so it only posts on state CHANGES
// (down -> up, up -> down), not on every tick.
package main

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// healthState tracks the last-known state of one target.
type healthState struct {
	mu      sync.Mutex
	up      map[string]bool // url -> last known up
	started time.Time
}

// healthChecker runs the periodic loop.
type healthChecker struct {
	session *discordgo.Session
	state   *healthState
	cfg     HealthCheckConfig
}

// startHealthChecker launches the background loop if enabled in config.
func startHealthChecker(ctx context.Context, s *discordgo.Session) {
	cfg, err := loadConfig()
	if err != nil || !cfg.HealthCheck.Enabled {
		return
	}
	interval := cfg.HealthCheck.Interval
	if interval <= 0 {
		interval = 10
	}
	hc := &healthChecker{
		session: s,
		state:   &healthState{up: map[string]bool{}, started: time.Now()},
		cfg:     cfg.HealthCheck,
	}
	log.Printf("health checker started: %d target(s), every %d min", len(cfg.HealthCheck.Targets), interval)
	hc.loop(ctx, time.Duration(interval)*time.Minute)
}

// loop ticks every interval and runs a check pass.
func (hc *healthChecker) loop(ctx context.Context, interval time.Duration) {
	hc.checkOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			hc.checkOnce()
		case <-ctx.Done():
			log.Println("health checker stopped")
			return
		}
	}
}

// checkOnce probes every target concurrently and posts state changes.
func (hc *healthChecker) checkOnce() {
	var wg sync.WaitGroup
	for _, url := range hc.cfg.Targets {
		url := url
		wg.Add(1)
		go func() {
			defer wg.Done()
			hc.checkTarget(url)
		}()
	}
	wg.Wait()
}

// checkTarget probes one URL and posts to #logs on a state change.
func (hc *healthChecker) checkTarget(url string) {
	up := probe(url)
	hc.state.mu.Lock()
	wasUp, known := hc.state.up[url]
	hc.state.up[url] = up
	hc.state.mu.Unlock()

	if !known || wasUp == up {
		return // no change (or first check — don't spam on startup)
	}

	dl := newLogger(hc.session)
	if up {
		dl.log("INFO", "✅ **"+url+"** is back UP ("+formatTimestamp()+")")
	} else {
		dl.log("ERROR", "🛑 **"+url+"** is DOWN ("+formatTimestamp()+")")
	}
}

// probe does a GET with a short timeout and reports reachability.
func probe(url string) bool {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Treat 2xx/3xx as up.
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
