package main

import (
	"context"
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
		{
			name: "Buckeye Broadband IP Network Manager - Local NW Ohio",
			job: JobPosting{
				Title:       "Manager, IP Network Services",
				Company:     "Buckeye Broadband",
				Location:    "Northwood Offices",
				Description: "Lead IP network operations, routing, disaster recovery, and infrastructure across Northwest Ohio.",
			},
			expectMatch: true,
			minScore:    60,
			mustHaveTag: "Local NW Ohio / Toledo Area",
		},
		{
			name: "FIT Technologies Support Technician - Local Toledo",
			job: JobPosting{
				Title:       "Technical Support Services Technician I",
				Company:     "FIT Technologies",
				Location:    "Toledo, OH",
				Description: "Provide tier 2 systems administration and network support for enterprise clients.",
			},
			expectMatch: true,
			minScore:    50,
			mustHaveTag: "Local NW Ohio / Toledo Area",
		},
		{
			name: "Huntington Bank Mainframe Systems Engineer",
			job: JobPosting{
				Title:       "Senior Mainframe Systems/Platform Engineer",
				Company:     "Huntington Bank",
				Location:    "Columbus, OH",
				Description: "Engineering leadership over systems platforms and infrastructure.",
			},
			expectMatch: true,
			minScore:    50,
			mustHaveTag: "Ohio Regional / Hybrid",
		},
		{
			name: "Hospital Food Service Aide - Disqualify",
			job: JobPosting{
				Title:       "Dietary - Patient Food Service Aide",
				Company:     "BVHS",
				Location:    "Findlay, OH",
				Description: "Prepare patient meals and deliver dietary trays.",
			},
			expectMatch: false,
			minScore:    0,
		},
		{
			name: "Branch Bank Teller - Disqualify",
			job: JobPosting{
				Title:       "Personal Banker Associate I",
				Company:     "Fifth Third Bank",
				Location:    "Toledo, OH",
				Description: "Open customer checking accounts and process teller transactions.",
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

func TestFetchPaylocityJobs(t *testing.T) {
	ctx := context.Background()
	htmlMock := `<!DOCTYPE html>
<html>
<head><title>Buckeye Broadband Careers</title></head>
<body>
<script>
window.pageData = {
  "Jobs": [
    {
      "JobId": 12345,
      "JobTitle": "Manager, IP Network Services",
      "Location": "Northwood Offices",
      "Department": "Network Engineering",
      "PublicJobDescription": "Oversee IP networking, infrastructure, routing protocols, and disaster recovery.",
      "PublishedDate": "2026-11-01T10:00:00"
    }
  ]
};
</script>
</body>
</html>`

	client := &http.Client{
		Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return notifierResponse(req, http.StatusOK, htmlMock), nil
		}),
	}

	jobs, err := fetchPaylocityJobs(ctx, client, "https://recruiting.paylocity.com/recruiting/jobs/All/b3d942e8-fa79-42c5-8d17-e7c7cae38ccc/Buckeye-Broadband")
	if err != nil {
		t.Fatalf("fetchPaylocityJobs failed: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	j := jobs[0]
	if j.ID != "paylocity-12345" {
		t.Errorf("expected ID paylocity-12345, got %s", j.ID)
	}
	if j.Title != "Manager, IP Network Services" {
		t.Errorf("expected title Manager, IP Network Services, got %s", j.Title)
	}
	if j.Company != "Buckeye Broadband" {
		t.Errorf("expected Buckeye Broadband, got %s", j.Company)
	}
	if j.URL != "https://recruiting.paylocity.com/recruiting/jobs/Details/12345" {
		t.Errorf("unexpected URL: %s", j.URL)
	}
	if j.Location != "Northwood Offices" {
		t.Errorf("expected Northwood Offices, got %s", j.Location)
	}
	if len(j.Tags) == 0 || j.Tags[0] != "Network Engineering" {
		t.Errorf("expected tag Network Engineering, got %v", j.Tags)
	}
}

func TestFetchWorkdayJobs(t *testing.T) {
	ctx := context.Background()
	jsonMock := `{
  "total": 1,
  "jobPostings": [
    {
      "title": "Director of IT Operations",
      "externalPath": "/job/Toledo-OH/Director-IT_R999",
      "locationsText": "Toledo, OH",
      "postedOn": "Posted 1 Day Ago",
      "bulletFields": ["Full time", "Leadership", "R999"]
    }
  ]
}`

	client := &http.Client{
		Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return notifierResponse(req, http.StatusOK, jsonMock), nil
		}),
	}

	jobs, err := fetchWorkdayJobs(ctx, client, "promedica", "External_Careers", "wd12", "ProMedica")
	if err != nil {
		t.Fatalf("fetchWorkdayJobs failed: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	j := jobs[0]
	if j.ID != "workday-promedica-Toledo-OH/Director-IT_R999" {
		t.Errorf("unexpected ID: %s", j.ID)
	}
	if j.Title != "Director of IT Operations" {
		t.Errorf("unexpected title: %s", j.Title)
	}
	if j.Company != "ProMedica" {
		t.Errorf("unexpected company: %s", j.Company)
	}
	if j.Location != "Toledo, OH" {
		t.Errorf("unexpected location: %s", j.Location)
	}
	if !strings.Contains(j.URL, "promedica.wd12.myworkdayjobs.com/External_Careers/job/Toledo-OH/Director-IT_R999") {
		t.Errorf("unexpected URL: %s", j.URL)
	}
}

func TestFetchBambooHRJobs(t *testing.T) {
	ctx := context.Background()
	jsonMock := `{
  "result": [
    {
      "id": "cnwr-501",
      "jobOpeningName": "Tier 2 Systems Administrator",
      "departmentLabel": "Technical Operations",
      "jobType": "Full-Time",
      "location": {
        "city": "Toledo",
        "state": "OH"
      }
    }
  ]
}`

	client := &http.Client{
		Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return notifierResponse(req, http.StatusOK, jsonMock), nil
		}),
	}

	jobs, err := fetchBambooHRJobs(ctx, client, "cnwr", "CNWR Managed IT Services")
	if err != nil {
		t.Fatalf("fetchBambooHRJobs failed: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	j := jobs[0]
	if j.ID != "bamboo-cnwr-cnwr-501" {
		t.Errorf("unexpected ID: %s", j.ID)
	}
	if j.Title != "Tier 2 Systems Administrator" {
		t.Errorf("unexpected title: %s", j.Title)
	}
	if j.Location != "Toledo, OH" {
		t.Errorf("unexpected location: %s", j.Location)
	}
	if j.URL != "https://cnwr.bamboohr.com/careers/cnwr-501" {
		t.Errorf("unexpected URL: %s", j.URL)
	}
}

func TestFetchADPJobs(t *testing.T) {
	ctx := context.Background()
	jsonMock := `{
  "jobRequisitions": [
    {
      "itemID": "adp-900",
      "requisitionTitle": "Technical Support Services Technician I",
      "requisitionDescription": "Provide systems and desktop infrastructure support in NW Ohio.",
      "requisitionLocations": [
        {
          "nameCode": { "shortName": "Toledo, OH" },
          "address": {
            "cityName": "Toledo",
            "countrySubdivisionLevel1": { "codeValue": "OH" }
          }
        }
      ]
    }
  ]
}`

	client := &http.Client{
		Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return notifierResponse(req, http.StatusOK, jsonMock), nil
		}),
	}

	jobs, err := fetchADPJobs(ctx, client, "cid123", "ccId123", "FIT Technologies")
	if err != nil {
		t.Fatalf("fetchADPJobs failed: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	j := jobs[0]
	if j.ID != "adp-adp-900" {
		t.Errorf("unexpected ID: %s", j.ID)
	}
	if j.Title != "Technical Support Services Technician I" {
		t.Errorf("unexpected title: %s", j.Title)
	}
	if j.Location != "Toledo, OH" {
		t.Errorf("unexpected location: %s", j.Location)
	}
	if !strings.Contains(j.URL, "jobId=adp-900") {
		t.Errorf("unexpected URL: %s", j.URL)
	}
}

func TestFetchTaleoJobs(t *testing.T) {
	ctx := context.Background()
	htmlMock := `<!DOCTYPE html>
<html>
<body>
<table>
  <tr>
    <td><a href="/phg02/ats/careers/v2/viewRequisition?org=BVHS&cws=40&rid=3001">Clinical Systems Engineer</a></td>
    <td>Information Technology</td>
  </tr>
</table>
</body>
</html>`

	client := &http.Client{
		Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return notifierResponse(req, http.StatusOK, htmlMock), nil
		}),
	}

	jobs, err := fetchTaleoJobs(ctx, client, "https://phg.tbe.taleo.net/phg02/ats/careers/v2/searchResults?org=BVHS&cws=40", "Blanchard Valley Health System")
	if err != nil {
		t.Fatalf("fetchTaleoJobs failed: %v", err)
	}

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	j := jobs[0]
	if j.ID != "taleo-bvhs-3001" {
		t.Errorf("unexpected ID: %s", j.ID)
	}
	if j.Title != "Clinical Systems Engineer" {
		t.Errorf("unexpected title: %s", j.Title)
	}
	if j.Company != "Blanchard Valley Health System" {
		t.Errorf("unexpected company: %s", j.Company)
	}
	if j.URL != "https://phg.tbe.taleo.net/phg02/ats/careers/v2/viewRequisition?org=BVHS&cws=40&rid=3001" {
		t.Errorf("unexpected URL: %s", j.URL)
	}
}

func TestScrapeCustomJobURLRouting(t *testing.T) {
	ctx := context.Background()
	client := &http.Client{
		Transport: notifierRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			urlStr := req.URL.String()
			if strings.Contains(urlStr, "paylocity.com") {
				return notifierResponse(req, http.StatusOK, `window.pageData = {"Jobs": [{"JobId": 1, "JobTitle": "NOC Analyst", "Location": "Northwood", "JobDescription": "NOC support"}]};`), nil
			}
			if strings.Contains(urlStr, "myworkdayjobs.com") {
				return notifierResponse(req, http.StatusOK, `{"total": 1, "jobPostings": [{"title": "IT Systems Architect", "externalPath": "/job/123", "locationsText": "Remote, USA"}]}`), nil
			}
			if strings.Contains(urlStr, "bamboohr.com") {
				return notifierResponse(req, http.StatusOK, `{"result": [{"id": 2, "jobOpeningName": "Network Admin", "location": {"city": "Toledo", "state": "OH"}}]}`), nil
			}
			if strings.Contains(urlStr, "adp.com") {
				return notifierResponse(req, http.StatusOK, `{"jobRequisitions": [{"itemID": "3", "requisitionTitle": "Cloud Engineer", "requisitionLocations": [{"address": {"cityName": "Cleveland", "countrySubdivisionLevel1": {"codeValue": "OH"}}}]}]}`), nil
			}
			if strings.Contains(urlStr, "taleo.net") {
				return notifierResponse(req, http.StatusOK, `<a href="/viewRequisition?org=BVHS&cws=40&rid=4">IT Support Tech</a>`), nil
			}
			return notifierResponse(req, http.StatusOK, `{"jobs": []}`), nil
		}),
	}

	targets := []string{
		"https://recruiting.paylocity.com/recruiting/jobs/All/b3d942e8-fa79-42c5-8d17-e7c7cae38ccc/Buckeye-Broadband",
		"https://www.promedica.org/promedicacareers",
		"https://www.53.com/content/fifth-third/en/careers.html",
		"https://www.key.com/about/careers.html",
		"https://www.huntington.com/careers",
		"https://jobs.cvshealth.com/us/en/home",
		"https://cnwr.com/about/employment",
		"https://www.fittechnologies.com/careers",
		"https://bvhealthsystem.org/careers",
	}

	for _, u := range targets {
		jobs, err := scrapeCustomJobURL(ctx, client, u)
		if err != nil {
			t.Errorf("scrapeCustomJobURL(%q) returned error: %v", u, err)
		}
		if len(jobs) == 0 {
			t.Errorf("scrapeCustomJobURL(%q) returned 0 jobs", u)
		}
	}
}

func TestCustomURLJobNotificationEndToEnd(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sunfire_job_custom_e2e_*")
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
	jobCache.Initialized = false
	jobCache.SeenJobs = make(map[string]string)
	jobCacheMu.Unlock()

	settings := &GuildSettings{
		JobsEnabled:   true,
		JobsChannelID: "test-jobs-channel",
		JobsURLs: []string{
			"https://recruiting.paylocity.com/recruiting/jobs/All/b3d942e8-fa79-42c5-8d17-e7c7cae38ccc/Buckeye-Broadband",
		},
	}

	paylocityHTML := `<!DOCTYPE html><html><body><script>
window.pageData = {
  "Jobs": [
    {
      "JobId": 8888,
      "JobTitle": "Manager, IP Network Services",
      "Location": "Northwood Offices",
      "Department": "Network Operations",
      "PublicJobDescription": "Lead enterprise IP network systems, Hyper-V, Windows Server, and disaster recovery.",
      "PublishedDate": "2026-11-01T12:00:00"
    }
  ]
};
</script></body></html>`

	setNotifierTestState(t, settings, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.String(), "paylocity.com") {
			return notifierResponse(req, http.StatusOK, paylocityHTML), nil
		}
		if strings.Contains(req.URL.String(), "weworkremotely.com") {
			return notifierResponse(req, http.StatusOK, `<rss version="2.0"><channel><title>WWR</title></channel></rss>`), nil
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
		return &discordgo.Message{ID: "msg-custom-test"}, nil
	}

	checkJobUpdates(nil, &Config{
		Guild: *settings,
	})

	if len(sentMessages) != 1 {
		t.Fatalf("expected 1 message posted on initial run, got %d", len(sentMessages))
	}
	if len(sentMessages[0].Embeds) == 0 {
		t.Fatal("expected embed in sent message")
	}

	embed := sentMessages[0].Embeds[0]
	if !strings.Contains(embed.Title, "Manager, IP Network Services") {
		t.Errorf("expected Title to contain Manager, IP Network Services, got: %s", embed.Title)
	}

	// Verify local NW Ohio highlight is included
	foundLocal := false
	for _, f := range embed.Fields {
		if strings.Contains(f.Value, "Local NW Ohio") {
			foundLocal = true
			break
		}
	}
	if !foundLocal {
		t.Errorf("expected Local NW Ohio highlight in embed fields: %+v", embed.Fields)
	}
}

