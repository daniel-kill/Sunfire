package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestEvaluateJobMatch(t *testing.T) {
	tests := []struct {
		name         string
		job          JobPosting
		expectMatch  bool
		minScore     int
		mustHaveTag  string
	}{
		{
			name: "Senior IT Manager - High Match",
			job: JobPosting{
				Title:       "Senior IT Manager",
				Company:     "Acme Corp",
				Location:    "Remote (US)",
				Description: "Direct enterprise IT operations, infrastructure modernization, disaster recovery, and compliance across systems.",
			},
			expectMatch: true,
			minScore:    50,
			mustHaveTag: "IT / Infrastructure Leadership",
		},
		{
			name: "DevOps & Infrastructure Lead",
			job: JobPosting{
				Title:       "Lead DevOps Engineer",
				Company:     "CloudScale Inc",
				Location:    "100% Remote, USA",
				Description: "Lead our cloud infrastructure with Docker, Linux systemd, and PowerShell/Bash automation routines.",
			},
			expectMatch: true,
			minScore:    50,
			mustHaveTag: "DevOps / Infrastructure Engineering",
		},
		{
			name: "Senior Go / Golang Systems Engineer",
			job: JobPosting{
				Title:       "Senior Golang Engineer",
				Company:     "FastAPI Tech",
				Location:    "Worldwide Remote",
				Description: "Build distributed backend daemons in Go (Golang) on Linux Debian servers with Docker containerization.",
			},
			expectMatch: true,
			minScore:    60,
			mustHaveTag: "Go / Golang Primary",
		},
		{
			name: "Foreign Only Location - Should Reject",
			job: JobPosting{
				Title:       "Senior DevOps Engineer",
				Company:     "Berlin Systems",
				Location:    "Germany only",
				Description: "Manage Linux and Docker infrastructure remotely within Germany.",
			},
			expectMatch: false,
			minScore:    0,
		},
		{
			name: "UK Only Location - Should Reject",
			job: JobPosting{
				Title:       "IT Operations Manager",
				Company:     "London Tech",
				Location:    "UK Only",
				Description: "Manage corporate IT operations across UK branches.",
			},
			expectMatch: false,
			minScore:    0,
		},
		{
			name: "Junior Intern - Should Reject",
			job: JobPosting{
				Title:       "Junior DevOps Intern",
				Company:     "Starter Co",
				Location:    "Remote (USA)",
				Description: "Learn basic bash and docker commands in a student internship.",
			},
			expectMatch: false,
			minScore:    0,
		},
		{
			name: "Irrelevant Sales Role - Should Reject",
			job: JobPosting{
				Title:       "Account Executive - Enterprise Sales",
				Company:     "SalesFlow",
				Location:    "Remote (USA)",
				Description: "Cold call clients and manage sales CRM pipelines.",
			},
			expectMatch: false,
			minScore:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score, highlights, match := evaluateJobMatch(tc.job)
			if match != tc.expectMatch {
				t.Errorf("expected match=%v, got %v (score=%d, highlights=%v)", tc.expectMatch, match, score, highlights)
			}
			if tc.expectMatch && score < tc.minScore {
				t.Errorf("expected score >= %d, got %d", tc.minScore, score)
			}
			if tc.mustHaveTag != "" {
				found := false
				for _, h := range highlights {
					if strings.Contains(h, tc.mustHaveTag) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected highlight %q in %v", tc.mustHaveTag, highlights)
				}
			}
		})
	}
}

func TestCleanHTMLDescription(t *testing.T) {
	raw := `<p>We are hiring a <strong>Senior Systems Engineer</strong> &amp; IT Manager.</p><br/><ul><li>Linux</li><li>Go</li></ul>`
	cleaned := cleanHTMLDescription(raw, 200)

	if strings.Contains(cleaned, "<p>") || strings.Contains(cleaned, "<strong>") || strings.Contains(cleaned, "&amp;") {
		t.Fatalf("cleaned string still contains HTML: %s", cleaned)
	}

	expectedPrefix := "We are hiring a Senior Systems Engineer & IT Manager. Linux Go"
	if !strings.HasPrefix(cleaned, expectedPrefix) {
		t.Errorf("expected prefix %q, got %q", expectedPrefix, cleaned)
	}

	// Test truncation
	truncated := cleanHTMLDescription(raw, 30)
	if len(truncated) > 30 {
		t.Errorf("expected length <= 30, got %d (%q)", len(truncated), truncated)
	}
	if !strings.HasSuffix(truncated, "...") {
		t.Errorf("expected truncated string to end with '...', got %q", truncated)
	}
}

func TestBuildJobEmbed(t *testing.T) {
	job := JobPosting{
		ID:          "test-123",
		Source:      "We Work Remotely",
		Title:       "Senior Infrastructure Architect",
		Company:     "HyperV Solutions",
		URL:         "https://example.com/job/123",
		Location:    "100% Remote (US/Worldwide)",
		Salary:      "$150,000 - $180,000 / yr",
		Description: "Looking for an infrastructure leader experienced with Windows Server, Hyper-V, and Linux.",
		Score:       75,
		Highlights:  []string{"IT / Infrastructure Leadership", "Linux (Debian/Ubuntu)", "Windows Server / Hyper-V"},
		PublishedAt: time.Now(),
	}

	embed := buildJobEmbed(job)
	if embed == nil {
		t.Fatal("expected non-nil embed")
	}

	if embed.Title != "💼 Senior Infrastructure Architect" {
		t.Errorf("unexpected embed title: %s", embed.Title)
	}
	if embed.Author == nil || embed.Author.Name != "HyperV Solutions" {
		t.Errorf("unexpected author: %+v", embed.Author)
	}
	if len(embed.Fields) != 4 {
		t.Errorf("expected 4 fields, got %d", len(embed.Fields))
	}
}

func TestJobsCachePersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sunfire_job_cache_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldPath := jobCachePath
	jobCachePath = filepath.Join(tmpDir, "testJobCache.json")
	defer func() {
		jobCachePath = oldPath
	}()

	jobCacheMu.Lock()
	jobCache.Initialized = true
	jobCache.SeenJobs = map[string]string{
		"job-1": "2026-11-01T12:00:00Z",
		"job-2": "2026-11-01T13:00:00Z",
	}
	jobCacheMu.Unlock()

	if err := saveJobsCache(); err != nil {
		t.Fatalf("saveJobsCache failed: %v", err)
	}

	// Reset in-memory cache
	jobCacheMu.Lock()
	jobCache.Initialized = false
	jobCache.SeenJobs = make(map[string]string)
	jobCacheMu.Unlock()

	// Load cache from disk
	data, err := os.ReadFile(jobCachePath)
	if err != nil {
		t.Fatalf("read cache failed: %v", err)
	}

	jobCacheMu.Lock()
	if err := os.WriteFile(jobCachePath, data, 0644); err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	jobCacheMu.Unlock()

	initJobsCache()

	jobCacheMu.Lock()
	defer jobCacheMu.Unlock()
	if !jobCache.Initialized {
		t.Errorf("expected Initialized=true after reload")
	}
	if _, ok := jobCache.SeenJobs["job-1"]; !ok {
		t.Errorf("expected job-1 in SeenJobs")
	}
	if _, ok := jobCache.SeenJobs["job-2"]; !ok {
		t.Errorf("expected job-2 in SeenJobs")
	}
}

func TestCheckJobUpdatesEndToEnd(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sunfire_job_e2e_test_*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldPath := jobCachePath
	jobCachePath = filepath.Join(tmpDir, "testJobCache.json")
	defer func() {
		jobCachePath = oldPath
	}()

	// Reset cache
	jobCacheMu.Lock()
	jobCache.Initialized = false
	jobCache.SeenJobs = make(map[string]string)
	jobCacheMu.Unlock()

	settings := &GuildSettings{
		JobsEnabled:   true,
		JobsChannelID: "test-jobs-channel",
	}

	wwrXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
	<channel>
		<title>We Work Remotely</title>
		<item>
			<title>CloudCorp: Senior IT Manager</title>
			<description>Direct operations, Hyper-V, Windows Server, and disaster recovery infrastructure.</description>
			<link>https://weworkremotely.com/jobs/111</link>
			<guid>wwr-111</guid>
			<region>Remote, USA</region>
			<category>Management</category>
			<type>Full-Time</type>
		</item>
	</channel>
</rss>`

	setNotifierTestState(t, settings, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.String(), "weworkremotely.com") {
			return notifierResponse(req, http.StatusOK, wwrXML), nil
		}
		if strings.Contains(req.URL.String(), "jobicy.com") {
			return notifierResponse(req, http.StatusOK, `{"jobs": []}`), nil
		}
		if strings.Contains(req.URL.String(), "remotive.com") {
			return notifierResponse(req, http.StatusOK, `{"jobs": []}`), nil
		}
		if strings.Contains(req.URL.String(), "remoteok.com") {
			return notifierResponse(req, http.StatusOK, `[{"legal": "terms"}]`), nil
		}
		return notifierResponse(req, http.StatusOK, ""), nil
	})

	var sentMessages []*discordgo.MessageSend
	sendNotifierComplex = func(_ *discordgo.Session, channelID string, message *discordgo.MessageSend) (*discordgo.Message, error) {
		if channelID != "test-jobs-channel" {
			t.Fatalf("unexpected channel ID: %s", channelID)
		}
		sentMessages = append(sentMessages, message)
		return &discordgo.Message{ID: "msg-test"}, nil
	}

	// 1. Initial run: should initialize and post top matches (1 in our mock)
	checkJobUpdates(nil, &Config{})

	if len(sentMessages) != 1 {
		t.Fatalf("expected 1 message on initial welcome run, got %d", len(sentMessages))
	}
	if !strings.Contains(sentMessages[0].Content, "Remote Job Matcher Initialized") {
		t.Errorf("expected welcome header, got: %s", sentMessages[0].Content)
	}
	if len(sentMessages[0].Embeds) == 0 || !strings.Contains(sentMessages[0].Embeds[0].Title, "Senior IT Manager") {
		t.Fatalf("expected Senior IT Manager embed, got: %+v", sentMessages[0].Embeds)
	}

	jobCacheMu.Lock()
	if !jobCache.Initialized {
		t.Errorf("expected cache to be Initialized")
	}
	if _, seen := jobCache.SeenJobs["wwr-111"]; !seen {
		t.Errorf("expected wwr-111 to be in seen jobs")
	}
	jobCacheMu.Unlock()

	// 2. Second run without new jobs: should send 0 messages
	sentMessages = nil
	checkJobUpdates(nil, &Config{})
	if len(sentMessages) != 0 {
		t.Fatalf("expected 0 messages on second run with no new jobs, got %d", len(sentMessages))
	}
}

