package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	defaultJobsInterval = 24 * time.Hour
	minPassingJobScore  = 35
	maxCachedJobs       = 2500
)

// JobPosting represents a normalized job opening scraped from an external board.
type JobPosting struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Company     string    `json:"company"`
	URL         string    `json:"url"`
	Location    string    `json:"location"`
	Salary      string    `json:"salary"`
	Tags        []string  `json:"tags"`
	Description string    `json:"description"`
	PublishedAt time.Time `json:"publishedAt"`
	Score       int       `json:"score"`
	Highlights  []string  `json:"highlights"`
}

type jobCacheState struct {
	Initialized bool              `json:"initialized"`
	SeenJobs    map[string]string `json:"seenJobs"` // Map job ID/URL -> ISO8601 timestamp
}

var (
	jobCacheMu   sync.Mutex
	jobCache     jobCacheState
	jobCachePath string
	htmlTagRegex = regexp.MustCompile(`<[^>]*>`)
	spaceRegex   = regexp.MustCompile(`\s+`)
)

func initJobsCache() {
	jobCacheMu.Lock()
	defer jobCacheMu.Unlock()

	if jobCachePath == "" {
		jobCachePath = dataPath("jobNotifierCache.json")
	}
	jobCache.SeenJobs = make(map[string]string)

	data, err := os.ReadFile(jobCachePath)
	if err == nil {
		if err := json.Unmarshal(data, &jobCache); err != nil {
			log.Printf("Job notifier: parse cache %s: %v", jobCachePath, err)
			jobCache.SeenJobs = make(map[string]string)
		}
	} else if !os.IsNotExist(err) {
		log.Printf("Job notifier: read cache %s: %v", jobCachePath, err)
	}

	if jobCache.SeenJobs == nil {
		jobCache.SeenJobs = make(map[string]string)
	}
}

func saveJobsCache() error {
	if jobCachePath == "" {
		return fmt.Errorf("job cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(jobCachePath), 0755); err != nil {
		return fmt.Errorf("creating job cache directory: %w", err)
	}
	data, err := json.MarshalIndent(jobCache, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding job cache: %w", err)
	}
	if err := os.WriteFile(jobCachePath, data, 0644); err != nil {
		return fmt.Errorf("writing job cache %s: %w", jobCachePath, err)
	}
	return nil
}

func cleanHTMLDescription(raw string, maxLen int) string {
	stripped := htmlTagRegex.ReplaceAllString(raw, " ")
	unescaped := html.UnescapeString(stripped)
	cleaned := strings.TrimSpace(spaceRegex.ReplaceAllString(unescaped, " "))
	if maxLen > 0 && len(cleaned) > maxLen {
		return cleaned[:maxLen-3] + "..."
	}
	return cleaned
}

// isLocationEligible checks if a location is acceptable (US remote, nationwide, or NW Ohio / Ohio local/hybrid).
func isLocationEligible(loc string) bool {
	l := strings.ToLower(strings.TrimSpace(loc))
	if l == "" || l == "remote" || l == "anywhere" || l == "worldwide" || l == "global" || l == "virtual" {
		return true
	}

	// Exclusive foreign locations that explicitly bar US candidates
	foreignOnly := []string{
		"uk only", "united kingdom only", "europe only", "eu only", "emea only",
		"germany only", "canada only", "latam only", "apac only", "asia only",
		"australia only", "india only", "philippines only", "brazil only",
		"singapore only", "spain only", "france only", "poland only", "ireland only",
	}
	for _, f := range foreignOnly {
		if strings.Contains(l, f) {
			return false
		}
	}

	return true
}

// isUSRemoteEligible maintains backward compatibility with existing tests.
func isUSRemoteEligible(loc string) bool {
	return isLocationEligible(loc)
}

// evaluateJobMatch scores a job against Daniel Kill's resume and qualification profile.
func evaluateJobMatch(job JobPosting) (score int, highlights []string, matches bool) {
	titleLower := strings.ToLower(job.Title)
	descLower := strings.ToLower(job.Description)
	tagsLower := strings.ToLower(strings.Join(job.Tags, " "))
	locLower := strings.ToLower(job.Location)
	fullText := titleLower + " " + descLower + " " + tagsLower + " " + locLower

	// Location check: must be eligible (US remote, nationwide, or local Ohio)
	if !isLocationEligible(job.Location) {
		return 0, nil, false
	}

	// Technical keywords indicating candidate IT/software/systems roles
	isTechRole := strings.Contains(titleLower, "engineer") ||
		strings.Contains(titleLower, "architect") ||
		strings.Contains(titleLower, "developer") ||
		strings.Contains(titleLower, "manager") ||
		strings.Contains(titleLower, "director") ||
		strings.Contains(titleLower, "devops") ||
		strings.Contains(titleLower, "sysadmin") ||
		strings.Contains(titleLower, "systems") ||
		strings.Contains(titleLower, "network") ||
		strings.Contains(titleLower, "technician") ||
		strings.Contains(titleLower, "analyst") ||
		strings.Contains(titleLower, "support") ||
		strings.Contains(titleLower, "solutions") ||
		strings.Contains(titleLower, "infrastructure") ||
		strings.Contains(titleLower, "technology") ||
		strings.Contains(titleLower, "information technology") ||
		strings.Contains(titleLower, "it ") ||
		strings.HasPrefix(titleLower, "it ") ||
		strings.HasSuffix(titleLower, " it") ||
		strings.Contains(titleLower, "brainiac")

	// Strict Disqualifiers: intern, junior, clinical medical, branch banking tellers, etc.
	strictDisqualifiers := []string{
		"intern", "internship", "junior developer", "junior engineer",
		"account executive", "sales development", "customer service rep",
		"copywriter", "graphic designer", "paralegal", "receptionist",
		"registered nurse", "nurse", "nursing", "physician", "surgeon",
		"pharmacist", "pharmacy tech", "patient care", "dietary", "dental",
		"phlebotomist", "radiology", "respiratory", "occupational therapist",
		"medical assistant", "audiolog", "lactation", "food service",
		"personal banker", "teller", "branch banker", "mortgage loan",
	}
	for _, dq := range strictDisqualifiers {
		if strings.Contains(titleLower, dq) {
			if strings.Contains(titleLower, "intern") || strings.Contains(titleLower, "nurse") ||
				strings.Contains(titleLower, "teller") || strings.Contains(titleLower, "aide") ||
				strings.Contains(titleLower, "food service") || strings.Contains(titleLower, "dietary") ||
				!isTechRole {
				return 0, nil, false
			}
		}
	}

	// 1. Leadership & Management Target Roles (Highest priority)
	if strings.Contains(titleLower, "it manager") ||
		strings.Contains(titleLower, "director of it") ||
		strings.Contains(titleLower, "it director") ||
		strings.Contains(titleLower, "infrastructure manager") ||
		strings.Contains(titleLower, "systems manager") ||
		strings.Contains(titleLower, "engineering manager") ||
		strings.Contains(titleLower, "head of it") ||
		strings.Contains(titleLower, "head of infrastructure") ||
		strings.Contains(titleLower, "technical delivery manager") ||
		strings.Contains(titleLower, "it operations manager") ||
		strings.Contains(titleLower, "technology manager") ||
		strings.Contains(titleLower, "network manager") ||
		strings.Contains(titleLower, "manager, ip network") ||
		strings.Contains(titleLower, "noc manager") {
		score += 45
		highlights = append(highlights, "IT / Infrastructure Leadership")
	}

	// 2. DevOps, Systems Administration & Infrastructure Roles
	if strings.Contains(titleLower, "devops") ||
		strings.Contains(titleLower, "sysadmin") ||
		strings.Contains(titleLower, "systems administrator") ||
		strings.Contains(titleLower, "systems engineer") ||
		strings.Contains(titleLower, "infrastructure engineer") ||
		strings.Contains(titleLower, "platform engineer") ||
		strings.Contains(titleLower, "site reliability") ||
		strings.Contains(titleLower, "sre") ||
		strings.Contains(titleLower, "cloud engineer") ||
		strings.Contains(titleLower, "cloud architect") ||
		strings.Contains(titleLower, "solutions engineer") ||
		strings.Contains(titleLower, "network engineer") ||
		strings.Contains(titleLower, "systems analyst") ||
		strings.Contains(titleLower, "technical support") ||
		strings.Contains(titleLower, "help desk") ||
		strings.Contains(titleLower, "service desk") ||
		strings.Contains(titleLower, "brainiac") {
		score += 35
		highlights = append(highlights, "DevOps / Infrastructure Engineering")
	}

	// 3. Go / Golang match
	if strings.Contains(titleLower, "golang") || strings.Contains(titleLower, "go developer") || strings.Contains(titleLower, "go engineer") {
		score += 35
		highlights = append(highlights, "Go / Golang Primary")
	} else if strings.Contains(fullText, "golang") || strings.Contains(tagsLower, "golang") || strings.Contains(fullText, " go ") {
		score += 20
		highlights = append(highlights, "Go (Golang)")
	}

	// 4. Linux & Infrastructure Stack
	if strings.Contains(fullText, "linux") || strings.Contains(fullText, "ubuntu") || strings.Contains(fullText, "debian") || strings.Contains(fullText, "systemd") {
		score += 15
		highlights = append(highlights, "Linux (Debian/Ubuntu)")
	}

	// 5. Windows Server & Hyper-V Virtualization
	if strings.Contains(fullText, "windows server") || strings.Contains(fullText, "hyper-v") || strings.Contains(fullText, "active directory") {
		score += 15
		highlights = append(highlights, "Windows Server / Hyper-V")
	}

	// 6. Docker & Containerization
	if strings.Contains(fullText, "docker") || strings.Contains(fullText, "container") || strings.Contains(fullText, "kubernetes") {
		score += 15
		highlights = append(highlights, "Docker & Containers")
	}

	// 7. Scripting & Automation (PowerShell, Bash)
	if strings.Contains(fullText, "powershell") || strings.Contains(fullText, "pwsh") || strings.Contains(fullText, "bash") || strings.Contains(fullText, "automation") {
		score += 10
		highlights = append(highlights, "PowerShell / Automation")
	}

	// 8. AI & Intelligent Systems
	if strings.Contains(fullText, "ai systems") || strings.Contains(fullText, "llm") || strings.Contains(fullText, "generative ai") || strings.Contains(fullText, "machine learning") {
		score += 15
		highlights = append(highlights, "AI / LLM Systems")
	}

	// 9. Disaster Recovery, Compliance & Security Governance
	if strings.Contains(fullText, "disaster recovery") || strings.Contains(fullText, "business continuity") || strings.Contains(fullText, "dr/bc") || strings.Contains(fullText, "compliance") || strings.Contains(fullText, "cybersecurity") {
		score += 10
		highlights = append(highlights, "DR/BC & Governance")
	}

	// 10. Seniority Level
	if strings.Contains(titleLower, "senior") || strings.Contains(titleLower, "lead") || strings.Contains(titleLower, "staff") || strings.Contains(titleLower, "principal") || strings.Contains(titleLower, "architect") {
		score += 15
		highlights = append(highlights, "Senior / Lead Tier")
	}

	// 11. Geographic match bonuses
	if strings.Contains(locLower, "toledo") ||
		strings.Contains(locLower, "walbridge") ||
		strings.Contains(locLower, "perrysburg") ||
		strings.Contains(locLower, "northwood") ||
		strings.Contains(locLower, "findlay") ||
		strings.Contains(locLower, "bowling green") ||
		strings.Contains(locLower, "sylvania") ||
		strings.Contains(locLower, "maumee") ||
		strings.Contains(locLower, "fremont") ||
		strings.Contains(locLower, "oregon, oh") ||
		strings.Contains(locLower, "lucas county") ||
		strings.Contains(locLower, "wood county") ||
		strings.Contains(locLower, "hancock county") {
		score += 20
		highlights = append(highlights, "Local NW Ohio / Toledo Area")
	} else if strings.Contains(locLower, "cleveland") ||
		strings.Contains(locLower, "columbus") ||
		strings.Contains(locLower, "cincinnati") ||
		strings.Contains(locLower, "akron") ||
		strings.Contains(locLower, "ohio") ||
		strings.Contains(locLower, " oh") ||
		strings.Contains(locLower, ", oh") ||
		strings.Contains(locLower, "oh-") {
		score += 10
		highlights = append(highlights, "Ohio Regional / Hybrid")
	} else if strings.Contains(locLower, "remote") ||
		strings.Contains(locLower, "anywhere") ||
		strings.Contains(locLower, "worldwide") ||
		strings.Contains(locLower, "global") ||
		strings.Contains(locLower, "virtual") {
		score += 10
		highlights = append(highlights, "100% US Remote")
	}

	// Penalize purely unrelated mobile or frontend without backend/devops overlap
	if (strings.Contains(titleLower, "react native") || strings.Contains(titleLower, "ios") || strings.Contains(titleLower, "android") || strings.Contains(titleLower, "flutter")) &&
		!strings.Contains(titleLower, "devops") && !strings.Contains(titleLower, "manager") && !strings.Contains(titleLower, "backend") {
		score -= 40
	}

	return score, highlights, score >= minPassingJobScore
}

// ----------------------------------------------------------------------------
// Scrapers / Fetchers
// ----------------------------------------------------------------------------

// 1. We Work Remotely RSS
type wwrRSS struct {
	XMLName xml.Name  `xml:"rss"`
	Channel wwrChan   `xml:"channel"`
}

type wwrChan struct {
	Title string    `xml:"title"`
	Items []wwrItem `xml:"item"`
}

type wwrItem struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Region      string `xml:"region"`
	Category    string `xml:"category"`
	Type        string `xml:"type"`
}

func fetchWWRJobs(ctx context.Context, client *http.Client) ([]JobPosting, error) {
	feeds := []string{
		"https://weworkremotely.com/categories/remote-devops-sysadmin-jobs.rss",
		"https://weworkremotely.com/categories/remote-back-end-programming-jobs.rss",
		"https://weworkremotely.com/categories/remote-management-and-finance-jobs.rss",
	}

	var results []JobPosting
	for _, feedURL := range feeds {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; remote-job-matcher)")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Job notifier: WWR fetch error (%s): %v", feedURL, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			continue
		}

		var parsed wwrRSS
		if err := xml.Unmarshal(body, &parsed); err != nil {
			continue
		}

		for _, itm := range parsed.Channel.Items {
			title := itm.Title
			company := ""
			if parts := strings.SplitN(title, ":", 2); len(parts) == 2 {
				company = strings.TrimSpace(parts[0])
				title = strings.TrimSpace(parts[1])
			}

			id := itm.GUID
			if id == "" {
				id = itm.Link
			}

			pubTime, _ := time.Parse(time.RFC1123Z, itm.PubDate)
			if pubTime.IsZero() {
				pubTime, _ = time.Parse(time.RFC1123, itm.PubDate)
			}
			if pubTime.IsZero() {
				pubTime = time.Now()
			}

			loc := itm.Region
			if loc == "" {
				loc = "100% Remote"
			}

			results = append(results, JobPosting{
				ID:          id,
				Source:      "We Work Remotely",
				Title:       title,
				Company:     company,
				URL:         itm.Link,
				Location:    loc,
				Salary:      "Disclosed in listing",
				Description: cleanHTMLDescription(itm.Description, 400),
				PublishedAt: pubTime,
				Tags:        []string{itm.Category, itm.Type},
			})
		}
	}

	return results, nil
}

// 2. Remotive API
type remotiveResponse struct {
	Jobs []remotiveItem `json:"jobs"`
}

type remotiveItem struct {
	ID                         int64    `json:"id"`
	URL                        string   `json:"url"`
	Title                      string   `json:"title"`
	CompanyName                string   `json:"company_name"`
	Category                   string   `json:"category"`
	JobType                    string   `json:"job_type"`
	PublicationDate            string   `json:"publication_date"`
	CandidateRequiredLocation string   `json:"candidate_required_location"`
	Salary                     string   `json:"salary"`
	Description                string   `json:"description"`
	Tags                       []string `json:"tags"`
}

func fetchRemotiveJobs(ctx context.Context, client *http.Client) ([]JobPosting, error) {
	categories := []string{"devops", "software-dev", "management"}
	var results []JobPosting

	for _, cat := range categories {
		apiURL := fmt.Sprintf("https://remotive.com/api/remote-jobs?category=%s&limit=35", cat)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; remote-job-matcher)")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Job notifier: Remotive fetch error (%s): %v", cat, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			continue
		}

		var parsed remotiveResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			continue
		}

		for _, itm := range parsed.Jobs {
			pubTime, _ := time.Parse(time.RFC3339, itm.PublicationDate)
			if pubTime.IsZero() {
				pubTime, _ = time.Parse("2006-01-02T15:04:05", itm.PublicationDate)
			}
			if pubTime.IsZero() {
				pubTime = time.Now()
			}

			salary := itm.Salary
			if salary == "" {
				salary = "Disclosed in listing"
			}

			loc := itm.CandidateRequiredLocation
			if loc == "" {
				loc = "100% Remote (Worldwide)"
			}

			results = append(results, JobPosting{
				ID:          fmt.Sprintf("remotive-%d", itm.ID),
				Source:      "Remotive",
				Title:       itm.Title,
				Company:     itm.CompanyName,
				URL:         itm.URL,
				Location:    loc,
				Salary:      salary,
				Description: cleanHTMLDescription(itm.Description, 400),
				PublishedAt: pubTime,
				Tags:        itm.Tags,
			})
		}
	}

	return results, nil
}

// 3. Jobicy API
type jobicyResponse struct {
	Jobs []jobicyItem `json:"jobs"`
}

type jobicyItem struct {
	ID             int64    `json:"id"`
	URL            string   `json:"url"`
	JobTitle       string   `json:"jobTitle"`
	CompanyName    string   `json:"companyName"`
	JobIndustry    []string `json:"jobIndustry"`
	JobType        []string `json:"jobType"`
	JobGeo         string   `json:"jobGeo"`
	JobLevel       string   `json:"jobLevel"`
	JobExcerpt     string   `json:"jobExcerpt"`
	JobDescription string   `json:"jobDescription"`
	PubDate        string   `json:"pubDate"`
	SalaryMin      int64    `json:"salaryMin"`
	SalaryMax      int64    `json:"salaryMax"`
	SalaryCurrency string   `json:"salaryCurrency"`
	SalaryPeriod   string   `json:"salaryPeriod"`
}

func fetchJobicyJobs(ctx context.Context, client *http.Client) ([]JobPosting, error) {
	apiURL := "https://jobicy.com/api/v2/remote-jobs?count=50&geo=usa"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; remote-job-matcher)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jobicy fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || err != nil {
		return nil, fmt.Errorf("jobicy HTTP status %d: %v", resp.StatusCode, err)
	}

	var parsed jobicyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("jobicy unmarshal: %w", err)
	}

	var results []JobPosting
	for _, itm := range parsed.Jobs {
		pubTime, _ := time.Parse(time.RFC3339, itm.PubDate)
		if pubTime.IsZero() {
			pubTime = time.Now()
		}

		salary := "Disclosed in listing"
		if itm.SalaryMin > 0 && itm.SalaryMax > 0 {
			salary = fmt.Sprintf("$%d - $%d %s (%s)", itm.SalaryMin, itm.SalaryMax, itm.SalaryCurrency, itm.SalaryPeriod)
		} else if itm.SalaryMin > 0 {
			salary = fmt.Sprintf("$%d+ %s (%s)", itm.SalaryMin, itm.SalaryCurrency, itm.SalaryPeriod)
		}

		loc := itm.JobGeo
		if loc == "" {
			loc = "100% Remote (USA)"
		}

		desc := itm.JobExcerpt
		if desc == "" {
			desc = itm.JobDescription
		}

		results = append(results, JobPosting{
			ID:          fmt.Sprintf("jobicy-%d", itm.ID),
			Source:      "Jobicy",
			Title:       itm.JobTitle,
			Company:     itm.CompanyName,
			URL:         itm.URL,
			Location:    loc,
			Salary:      salary,
			Description: cleanHTMLDescription(desc, 400),
			PublishedAt: pubTime,
			Tags:        append(itm.JobIndustry, itm.JobLevel),
		})
	}

	return results, nil
}

// 4. RemoteOK API
type remoteOKItem struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`
	Company   string   `json:"company"`
	Position  string   `json:"position"`
	Tags      []string `json:"tags"`
	Desc      string   `json:"description"`
	Location  string   `json:"location"`
	SalaryMin int64    `json:"salary_min"`
	SalaryMax int64    `json:"salary_max"`
	URL       string   `json:"url"`
}

func fetchRemoteOKJobs(ctx context.Context, client *http.Client) ([]JobPosting, error) {
	apiURL := "https://remoteok.com/api"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; remote-job-matcher)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remoteok fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || err != nil {
		return nil, fmt.Errorf("remoteok HTTP status %d: %v", resp.StatusCode, err)
	}

	var rawItems []remoteOKItem
	if err := json.Unmarshal(body, &rawItems); err != nil {
		return nil, fmt.Errorf("remoteok unmarshal: %w", err)
	}

	var results []JobPosting
	for _, itm := range rawItems {
		if itm.ID == "" || itm.Position == "" {
			continue // metadata header object
		}

		pubTime, _ := time.Parse(time.RFC3339, itm.Date)
		if pubTime.IsZero() {
			pubTime = time.Now()
		}

		salary := "Disclosed in listing"
		if itm.SalaryMin > 0 && itm.SalaryMax > 0 {
			salary = fmt.Sprintf("$%d,000 - $%d,000 / yr", itm.SalaryMin/1000, itm.SalaryMax/1000)
		} else if itm.SalaryMin > 0 {
			salary = fmt.Sprintf("$%d,000+ / yr", itm.SalaryMin/1000)
		}

		loc := itm.Location
		if loc == "" {
			loc = "100% Remote"
		}

		url := itm.URL
		if !strings.HasPrefix(url, "http") {
			url = "https://remoteok.com/remote-jobs/" + itm.ID
		}

		results = append(results, JobPosting{
			ID:          fmt.Sprintf("remoteok-%s", itm.ID),
			Source:      "Remote OK",
			Title:       itm.Position,
			Company:     itm.Company,
			URL:         url,
			Location:    loc,
			Salary:      salary,
			Description: cleanHTMLDescription(itm.Desc, 400),
			PublishedAt: pubTime,
			Tags:        itm.Tags,
		})
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// Custom URL Scrapers & Multi-ATS Dispatcher
// ----------------------------------------------------------------------------

// fetchCustomURLJobs concurrently scrapes each URL in urls using appropriate ATS/HTML scrapers.
func fetchCustomURLJobs(ctx context.Context, client *http.Client, urls []string) []JobPosting {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []JobPosting
	)

	// Limit concurrent scraping to 5 workers
	sem := make(chan struct{}, 5)

	for _, targetURL := range urls {
		targetURL := strings.TrimSpace(targetURL)
		if targetURL == "" {
			continue
		}

		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			jobs, err := scrapeCustomJobURL(ctx, client, u)
			if err != nil {
				log.Printf("Job notifier: scrape custom URL %s error: %v", u, err)
				return
			}
			if len(jobs) > 0 {
				mu.Lock()
				results = append(results, jobs...)
				mu.Unlock()
				log.Printf("Job notifier: scraped %d jobs from %s", len(jobs), u)
			}
		}(targetURL)
	}

	wg.Wait()
	return results
}

// scrapeCustomJobURL dispatches scraping based on ATS / domain characteristics.
func scrapeCustomJobURL(ctx context.Context, client *http.Client, targetURL string) ([]JobPosting, error) {
	lowerURL := strings.ToLower(targetURL)

	// 1. Buckeye Broadband / Paylocity
	if strings.Contains(lowerURL, "recruiting.paylocity.com") {
		return fetchPaylocityJobs(ctx, client, targetURL)
	}

	// 2. ProMedica (Workday wd12)
	if strings.Contains(lowerURL, "promedica.org") || strings.Contains(lowerURL, "promedica.wd12.myworkdayjobs.com") {
		return fetchWorkdayJobs(ctx, client, "promedica", "External_Careers", "wd12", "ProMedica")
	}

	// 3. Fifth Third Bank (Workday wd5)
	if strings.Contains(lowerURL, "53.com") || strings.Contains(lowerURL, "fifththird.wd5.myworkdayjobs.com") {
		return fetchWorkdayJobs(ctx, client, "fifththird", "53careers", "wd5", "Fifth Third Bank")
	}

	// 4. KeyBank (Workday wd5)
	if strings.Contains(lowerURL, "key.com") || strings.Contains(lowerURL, "keybank.wd5.myworkdayjobs.com") {
		return fetchWorkdayJobs(ctx, client, "keybank", "External_Career_Site", "wd5", "KeyBank")
	}

	// 5. Huntington Bank (Workday wd12)
	if strings.Contains(lowerURL, "huntington.com") || strings.Contains(lowerURL, "huntington.wd12.myworkdayjobs.com") {
		return fetchWorkdayJobs(ctx, client, "huntington", "HNBcareers", "wd12", "Huntington Bank")
	}

	// 6. CVS Health (Workday wd1)
	if strings.Contains(lowerURL, "cvshealth.com") {
		return fetchWorkdayJobs(ctx, client, "cvshealth", "CVS_Health_Careers", "wd1", "CVS Health")
	}

	// 7. CNWR (BambooHR)
	if strings.Contains(lowerURL, "cnwr.com") || strings.Contains(lowerURL, "cnwr.bamboohr.com") {
		return fetchBambooHRJobs(ctx, client, "cnwr", "CNWR Managed IT Services")
	}

	// 8. FIT Technologies (ADP Workforce Now)
	if strings.Contains(lowerURL, "fittechnologies.com") || strings.Contains(lowerURL, "4116d4da-3047-443d-9816-3bf46f7aacbd") {
		return fetchADPJobs(ctx, client, "4116d4da-3047-443d-9816-3bf46f7aacbd", "19000101_000001", "FIT Technologies")
	}

	// 9. Blanchard Valley Health System (Taleo)
	if strings.Contains(lowerURL, "bvhealthsystem.org") || strings.Contains(lowerURL, "phg.tbe.taleo.net") {
		taleoURL := "https://phg.tbe.taleo.net/phg02/ats/careers/v2/searchResults?org=BVHS&cws=40"
		return fetchTaleoJobs(ctx, client, taleoURL, "Blanchard Valley Health System")
	}

	// 10. Generic Workday URL match (e.g. https://tenant.wdX.myworkdayjobs.com/site)
	wdRegex := regexp.MustCompile(`https?://([a-zA-Z0-9_\-]+)\.(wd[0-9]+)\.myworkdayjobs\.com/([a-zA-Z0-9_\-]+)`)
	if m := wdRegex.FindStringSubmatch(targetURL); len(m) == 4 {
		return fetchWorkdayJobs(ctx, client, m[1], m[3], m[2], strings.Title(m[1]))
	}

	// 11. Generic BambooHR URL match (e.g. https://subdomain.bamboohr.com/careers)
	bambooRegex := regexp.MustCompile(`https?://([a-zA-Z0-9_\-]+)\.bamboohr\.com`)
	if m := bambooRegex.FindStringSubmatch(targetURL); len(m) == 2 {
		return fetchBambooHRJobs(ctx, client, m[1], strings.Title(m[1]))
	}

	// 12. Generic RSS / XML feed
	if strings.HasSuffix(lowerURL, ".rss") || strings.HasSuffix(lowerURL, ".xml") {
		return fetchRSSJobs(ctx, client, targetURL, "RSS Job Feed")
	}

	// 13. Generic Webpage / JSON-LD / HTML Fallback
	return fetchGenericWebpageJobs(ctx, client, targetURL)
}

// ----------------------------------------------------------------------------
// Paylocity Scraper
// ----------------------------------------------------------------------------

type paylocityData struct {
	Jobs []struct {
		JobID                interface{} `json:"JobId"`
		JobTitle             string      `json:"JobTitle"`
		Location             string      `json:"Location"`
		Department           string      `json:"Department"`
		PublicJobDescription string      `json:"PublicJobDescription"`
		JobDescription       string      `json:"JobDescription"`
		PublishedDate        string      `json:"PublishedDate"`
	} `json:"Jobs"`
}

func formatJobID(id interface{}) string {
	switch v := id.(type) {
	case float64:
		return fmt.Sprintf("%.0f", v)
	case float32:
		return fmt.Sprintf("%.0f", v)
	case int, int32, int64:
		return fmt.Sprintf("%d", v)
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func fetchPaylocityJobs(ctx context.Context, client *http.Client, paylocityURL string) ([]JobPosting, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, paylocityURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; career-matcher)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("paylocity HTTP %d: %v", resp.StatusCode, err)
	}

	var pData paylocityData
	idx := bytes.Index(body, []byte("window.pageData = "))
	if idx == -1 {
		idx = bytes.Index(body, []byte("window.pageData="))
	}
	if idx == -1 {
		return nil, fmt.Errorf("window.pageData not found in Paylocity HTML")
	}

	start := idx + bytes.IndexByte(body[idx:], '=') + 1
	for start < len(body) && (body[start] == ' ' || body[start] == '\t' || body[start] == '\r' || body[start] == '\n') {
		start++
	}
	end := bytes.Index(body[start:], []byte("};"))
	if end == -1 {
		return nil, fmt.Errorf("terminating '};' not found for Paylocity pageData")
	}

	rawJSON := body[start : start+end+1]
	if err := json.Unmarshal(rawJSON, &pData); err != nil {
		return nil, fmt.Errorf("unmarshaling paylocity pageData: %w", err)
	}

	company := "Buckeye Broadband"
	if strings.Contains(paylocityURL, "Buckeye-Broadband") {
		company = "Buckeye Broadband"
	}

	var results []JobPosting
	for _, j := range pData.Jobs {
		jobID := formatJobID(j.JobID)
		if jobID == "" || j.JobTitle == "" {
			continue
		}

		desc := j.PublicJobDescription
		if desc == "" {
			desc = j.JobDescription
		}

		loc := j.Location
		if loc == "" {
			loc = "Toledo / Northwood, OH"
		}

		pubTime, _ := time.Parse("2006-01-02T15:04:05", j.PublishedDate)
		if pubTime.IsZero() {
			pubTime = time.Now()
		}

		detailURL := fmt.Sprintf("https://recruiting.paylocity.com/recruiting/jobs/Details/%s", jobID)

		var tags []string
		if j.Department != "" {
			tags = append(tags, j.Department)
		}

		results = append(results, JobPosting{
			ID:          fmt.Sprintf("paylocity-%s", jobID),
			Source:      company + " (Paylocity)",
			Title:       j.JobTitle,
			Company:     company,
			URL:         detailURL,
			Location:    loc,
			Salary:      "Disclosed in listing",
			Description: cleanHTMLDescription(desc, 400),
			PublishedAt: pubTime,
			Tags:        tags,
		})
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// Workday CXS Scraper
// ----------------------------------------------------------------------------

type workdayPayload struct {
	AppliedFacets map[string]interface{} `json:"appliedFacets"`
	Limit         int                    `json:"limit"`
	Offset        int                    `json:"offset"`
	SearchText    string                 `json:"searchText"`
}

type workdayResponse struct {
	Total       int `json:"total"`
	JobPostings []struct {
		Title         string   `json:"title"`
		ExternalPath  string   `json:"externalPath"`
		LocationsText string   `json:"locationsText"`
		PostedOn      string   `json:"postedOn"`
		BulletFields  []string `json:"bulletFields"`
	} `json:"jobPostings"`
}

func fetchWorkdayJobs(ctx context.Context, client *http.Client, tenant, site, wd, company string) ([]JobPosting, error) {
	endpoint := fmt.Sprintf("https://%s.%s.myworkdayjobs.com/wday/cxs/%s/%s/jobs", tenant, wd, tenant, site)

	queries := []string{""}
	if company == "CVS Health" || company == "Fifth Third Bank" || company == "Huntington Bank" {
		queries = append(queries, "technology", "engineer")
	}

	seenPaths := make(map[string]bool)
	var results []JobPosting

	for _, q := range queries {
		payloadObj := workdayPayload{
			AppliedFacets: map[string]interface{}{},
			Limit:         50,
			Offset:        0,
			SearchText:    q,
		}
		pBytes, err := json.Marshal(payloadObj)
		if err != nil {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(pBytes))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; career-matcher)")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			continue
		}

		var wdResp workdayResponse
		if err := json.Unmarshal(body, &wdResp); err != nil {
			continue
		}

		for _, p := range wdResp.JobPostings {
			if p.ExternalPath == "" || seenPaths[p.ExternalPath] {
				continue
			}
			seenPaths[p.ExternalPath] = true

			jobURL := fmt.Sprintf("https://%s.%s.myworkdayjobs.com/%s%s", tenant, wd, site, p.ExternalPath)
			jobID := fmt.Sprintf("workday-%s-%s", tenant, strings.TrimPrefix(p.ExternalPath, "/job/"))

			loc := p.LocationsText
			if loc == "" {
				loc = "United States"
			}

			desc := strings.Join(p.BulletFields, " • ")
			if p.PostedOn != "" {
				desc = p.PostedOn + " • " + desc
			}

			results = append(results, JobPosting{
				ID:          jobID,
				Source:      company + " (Workday)",
				Title:       p.Title,
				Company:     company,
				URL:         jobURL,
				Location:    loc,
				Salary:      "Disclosed in listing",
				Description: cleanHTMLDescription(desc, 400),
				PublishedAt: time.Now(),
				Tags:        p.BulletFields,
			})
		}
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// BambooHR Scraper
// ----------------------------------------------------------------------------

type bambooResponse struct {
	Result []struct {
		ID              interface{} `json:"id"`
		JobOpeningName  string      `json:"jobOpeningName"`
		DepartmentLabel string      `json:"departmentLabel"`
		JobType         string      `json:"jobType"`
		Location        struct {
			City  string `json:"city"`
			State string `json:"state"`
		} `json:"location"`
	} `json:"result"`
}

func fetchBambooHRJobs(ctx context.Context, client *http.Client, subdomain, company string) ([]JobPosting, error) {
	url := fmt.Sprintf("https://%s.bamboohr.com/careers/list", subdomain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; career-matcher)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bamboohr HTTP %d: %v", resp.StatusCode, err)
	}

	var bResp bambooResponse
	if err := json.Unmarshal(body, &bResp); err != nil {
		return nil, fmt.Errorf("unmarshaling bamboohr response: %w", err)
	}

	var results []JobPosting
	for _, itm := range bResp.Result {
		idStr := formatJobID(itm.ID)
		if idStr == "" || itm.JobOpeningName == "" {
			continue
		}

		loc := fmt.Sprintf("%s, %s", itm.Location.City, itm.Location.State)
		if itm.Location.City == "" && itm.Location.State == "" {
			loc = "Toledo, OH"
		}

		detailURL := fmt.Sprintf("https://%s.bamboohr.com/careers/%s", subdomain, idStr)

		var tags []string
		if itm.DepartmentLabel != "" {
			tags = append(tags, itm.DepartmentLabel)
		}
		if itm.JobType != "" {
			tags = append(tags, itm.JobType)
		}

		results = append(results, JobPosting{
			ID:          fmt.Sprintf("bamboo-%s-%s", subdomain, idStr),
			Source:      company + " (BambooHR)",
			Title:       itm.JobOpeningName,
			Company:     company,
			URL:         detailURL,
			Location:    loc,
			Salary:      "Disclosed in listing",
			Description: fmt.Sprintf("%s • %s", itm.DepartmentLabel, itm.JobType),
			PublishedAt: time.Now(),
			Tags:        tags,
		})
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// ADP Workforce Now Scraper
// ----------------------------------------------------------------------------

type adpResponse struct {
	JobRequisitions []struct {
		ItemID                 string `json:"itemID"`
		RequisitionTitle       string `json:"requisitionTitle"`
		RequisitionDescription string `json:"requisitionDescription"`
		RequisitionLocations   []struct {
			NameCode struct {
				ShortName string `json:"shortName"`
			} `json:"nameCode"`
			Address struct {
				CityName                 string `json:"cityName"`
				CountrySubdivisionLevel1 struct {
					CodeValue string `json:"codeValue"`
				} `json:"countrySubdivisionLevel1"`
			} `json:"address"`
		} `json:"requisitionLocations"`
	} `json:"jobRequisitions"`
}

func fetchADPJobs(ctx context.Context, client *http.Client, cid, ccId, company string) ([]JobPosting, error) {
	url := fmt.Sprintf("https://workforcenow.adp.com/mascsr/default/careercenter/public/events/staffing/v1/job-requisitions?cid=%s&ccId=%s&lang=en_US", cid, ccId)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; career-matcher)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("adp HTTP %d: %v", resp.StatusCode, err)
	}

	var aResp adpResponse
	if err := json.Unmarshal(body, &aResp); err != nil {
		return nil, fmt.Errorf("unmarshaling adp response: %w", err)
	}

	var results []JobPosting
	for _, req := range aResp.JobRequisitions {
		if req.ItemID == "" || req.RequisitionTitle == "" {
			continue
		}

		loc := "Cleveland / Toledo, OH"
		if len(req.RequisitionLocations) > 0 {
			first := req.RequisitionLocations[0]
			if first.Address.CityName != "" && first.Address.CountrySubdivisionLevel1.CodeValue != "" {
				loc = fmt.Sprintf("%s, %s", first.Address.CityName, first.Address.CountrySubdivisionLevel1.CodeValue)
			} else if first.NameCode.ShortName != "" {
				loc = first.NameCode.ShortName
			}
		}

		detailURL := fmt.Sprintf("https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html?cid=%s&ccId=%s&jobId=%s&lang=en_US", cid, ccId, req.ItemID)

		results = append(results, JobPosting{
			ID:          fmt.Sprintf("adp-%s", req.ItemID),
			Source:      company + " (ADP)",
			Title:       req.RequisitionTitle,
			Company:     company,
			URL:         detailURL,
			Location:    loc,
			Salary:      "Disclosed in listing",
			Description: cleanHTMLDescription(req.RequisitionDescription, 400),
			PublishedAt: time.Now(),
		})
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// Taleo Scraper
// ----------------------------------------------------------------------------

var taleoLinkRegex = regexp.MustCompile(`<a[^>]+href="([^"]*viewRequisition[^"]*)"[^>]*>([\s\S]*?)</a>`)
var taleoRidRegex = regexp.MustCompile(`rid=([0-9]+)`)

func fetchTaleoJobs(ctx context.Context, client *http.Client, taleoURL, company string) ([]JobPosting, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, taleoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("taleo HTTP %d: %v", resp.StatusCode, err)
	}

	matches := taleoLinkRegex.FindAllSubmatch(body, -1)
	seenRids := make(map[string]bool)
	var results []JobPosting

	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		rawLink := string(m[1])
		rawTitle := cleanHTMLDescription(string(m[2]), 120)

		if rawTitle == "" || strings.EqualFold(rawTitle, "view") || strings.EqualFold(rawTitle, "apply") {
			continue
		}

		ridMatch := taleoRidRegex.FindStringSubmatch(rawLink)
		if len(ridMatch) < 2 {
			continue
		}
		rid := ridMatch[1]
		if seenRids[rid] {
			continue
		}
		seenRids[rid] = true

		detailURL := rawLink
		if !strings.HasPrefix(detailURL, "http") {
			detailURL = "https://phg.tbe.taleo.net" + detailURL
		}

		results = append(results, JobPosting{
			ID:          fmt.Sprintf("taleo-bvhs-%s", rid),
			Source:      company + " (Taleo)",
			Title:       rawTitle,
			Company:     company,
			URL:         detailURL,
			Location:    "Findlay, OH (BVHS)",
			Salary:      "Disclosed in listing",
			Description: fmt.Sprintf("Blanchard Valley Health System Requisition %s: %s", rid, rawTitle),
			PublishedAt: time.Now(),
		})
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// RSS Feed Scraper
// ----------------------------------------------------------------------------

type rssRoot struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Category    string `xml:"category"`
}

func fetchRSSJobs(ctx context.Context, client *http.Client, rssURL, company string) ([]JobPosting, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rssURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SunfireBot/1.0 (daniel@danielkill.online; career-matcher)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rss HTTP %d: %v", resp.StatusCode, err)
	}

	var root rssRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("xml unmarshal rss: %w", err)
	}

	var results []JobPosting
	for _, itm := range root.Channel.Items {
		id := itm.GUID
		if id == "" {
			id = itm.Link
		}
		if id == "" || itm.Title == "" {
			continue
		}

		pubTime, _ := time.Parse(time.RFC1123Z, itm.PubDate)
		if pubTime.IsZero() {
			pubTime, _ = time.Parse(time.RFC1123, itm.PubDate)
		}
		if pubTime.IsZero() {
			pubTime = time.Now()
		}

		var tags []string
		if itm.Category != "" {
			tags = append(tags, itm.Category)
		}

		results = append(results, JobPosting{
			ID:          id,
			Source:      company,
			Title:       cleanHTMLDescription(itm.Title, 120),
			Company:     company,
			URL:         itm.Link,
			Location:    "United States / Remote",
			Salary:      "Disclosed in listing",
			Description: cleanHTMLDescription(itm.Description, 400),
			PublishedAt: pubTime,
			Tags:        tags,
		})
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// Generic HTML / JSON-LD Fallback Scraper
// ----------------------------------------------------------------------------

type jsonLDJobPosting struct {
	Type               interface{} `json:"@type"`
	Title              string      `json:"title"`
	Description        string      `json:"description"`
	HiringOrganization struct {
		Name string `json:"name"`
	} `json:"hiringOrganization"`
	JobLocation struct {
		Address struct {
			AddressLocality string `json:"addressLocality"`
			AddressRegion   string `json:"addressRegion"`
		} `json:"address"`
	} `json:"jobLocation"`
	URL string `json:"url"`
}

var jsonLDScriptRegex = regexp.MustCompile(`<script[^>]+type=["']application/ld\+json["'][^>]*>([\s\S]*?)</script>`)
var genericJobLinkRegex = regexp.MustCompile(`<a[^>]+href="([^"]*(?:/job/|/careers/|/position/|/requisition/|/jobs/)[^"]*)"[^>]*>([\s\S]*?)</a>`)

func fetchGenericWebpageJobs(ctx context.Context, client *http.Client, pageURL string) ([]JobPosting, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webpage HTTP %d: %v", resp.StatusCode, err)
	}

	var results []JobPosting

	// Check JSON-LD
	jsonLDMatches := jsonLDScriptRegex.FindAllSubmatch(body, -1)
	for _, m := range jsonLDMatches {
		if len(m) < 2 {
			continue
		}
		var jp jsonLDJobPosting
		if err := json.Unmarshal(m[1], &jp); err == nil && jp.Title != "" {
			loc := fmt.Sprintf("%s, %s", jp.JobLocation.Address.AddressLocality, jp.JobLocation.Address.AddressRegion)
			if loc == ", " {
				loc = "Disclosed in listing"
			}
			jobURL := jp.URL
			if jobURL == "" {
				jobURL = pageURL
			}
			company := jp.HiringOrganization.Name
			if company == "" {
				company = "Career Listing"
			}
			results = append(results, JobPosting{
				ID:          fmt.Sprintf("jsonld-%s-%s", jp.Title, loc),
				Source:      "Web Career Listing",
				Title:       jp.Title,
				Company:     company,
				URL:         jobURL,
				Location:    loc,
				Salary:      "Disclosed in listing",
				Description: cleanHTMLDescription(jp.Description, 400),
				PublishedAt: time.Now(),
			})
		}
	}

	if len(results) > 0 {
		return results, nil
	}

	// Fallback to extracting job links
	linkMatches := genericJobLinkRegex.FindAllSubmatch(body, -1)
	seenHrefs := make(map[string]bool)
	for _, m := range linkMatches {
		if len(m) < 3 {
			continue
		}
		rawHref := string(m[1])
		rawTitle := cleanHTMLDescription(string(m[2]), 120)

		if len(rawTitle) < 5 || seenHrefs[rawHref] {
			continue
		}
		if strings.EqualFold(rawTitle, "apply now") || strings.EqualFold(rawTitle, "learn more") || strings.EqualFold(rawTitle, "read more") {
			continue
		}
		seenHrefs[rawHref] = true

		fullLink := rawHref
		if !strings.HasPrefix(fullLink, "http") {
			fullLink = pageURL + rawHref
		}

		results = append(results, JobPosting{
			ID:          fmt.Sprintf("web-%s", fullLink),
			Source:      "Web Career Board",
			Title:       rawTitle,
			Company:     "Career Listing",
			URL:         fullLink,
			Location:    "Disclosed in listing",
			Salary:      "Disclosed in listing",
			Description: rawTitle,
			PublishedAt: time.Now(),
		})
	}

	return results, nil
}

// ----------------------------------------------------------------------------
// Core Matcher & Dispatcher
// ----------------------------------------------------------------------------

func fetchAllCandidateJobs(ctx context.Context, client *http.Client, customURLs []string) []JobPosting {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allJobs []JobPosting
	)

	fetchers := []struct {
		name string
		fn   func(context.Context, *http.Client) ([]JobPosting, error)
	}{
		{"WeWorkRemotely", fetchWWRJobs},
		{"Remotive", fetchRemotiveJobs},
		{"Jobicy", fetchJobicyJobs},
		{"RemoteOK", fetchRemoteOKJobs},
	}

	for _, f := range fetchers {
		wg.Add(1)
		go func(name string, fetchFn func(context.Context, *http.Client) ([]JobPosting, error)) {
			defer wg.Done()
			jobs, err := fetchFn(ctx, client)
			if err != nil {
				log.Printf("Job notifier: [%s] error: %v", name, err)
				return
			}
			mu.Lock()
			allJobs = append(allJobs, jobs...)
			mu.Unlock()
			log.Printf("Job notifier: [%s] fetched %d jobs", name, len(jobs))
		}(f.name, f.fn)
	}

	if len(customURLs) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cJobs := fetchCustomURLJobs(ctx, client, customURLs)
			mu.Lock()
			allJobs = append(allJobs, cJobs...)
			mu.Unlock()
			log.Printf("Job notifier: [CustomURLs] fetched %d jobs from %d sources", len(cJobs), len(customURLs))
		}()
	}

	wg.Wait()

	// Deduplicate candidates across feeds and categories by Job ID or URL
	seen := make(map[string]bool)
	var deduped []JobPosting
	for _, j := range allJobs {
		key := j.ID
		if key == "" {
			key = j.URL
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, j)
	}

	return deduped
}

func buildJobEmbed(job JobPosting) *discordgo.MessageEmbed {
	highlightsStr := "None"
	if len(job.Highlights) > 0 {
		highlightsStr = strings.Join(job.Highlights, " • ")
	}

	company := job.Company
	if company == "" {
		company = "Remote Tech Employer"
	}

	desc := job.Description
	if len(desc) > 350 {
		desc = desc[:347] + "..."
	}

	locDisplay := job.Location
	if locDisplay == "" {
		locDisplay = "100% Remote (US)"
	}
	locFieldTitle := "📍 Workplace Location"
	if strings.Contains(strings.ToLower(locDisplay), "remote") {
		locFieldTitle = "🌐 Remote Status"
	}

	salaryDisplay := job.Salary
	if salaryDisplay == "" {
		salaryDisplay = "Disclosed in listing"
	}

	fields := []*discordgo.MessageEmbedField{
		{
			Name:   locFieldTitle,
			Value:  locDisplay,
			Inline: true,
		},
		{
			Name:   "💰 Compensation",
			Value:  salaryDisplay,
			Inline: true,
		},
		{
			Name:   "📡 Source",
			Value:  job.Source,
			Inline: true,
		},
		{
			Name:   "🎯 Match Profile & Highlights",
			Value:  fmt.Sprintf("**Score:** %d/100\n**Matched:** %s", job.Score, highlightsStr),
			Inline: false,
		},
	}

	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("💼 %s", job.Title),
		URL:         job.URL,
		Description: desc,
		Color:       0x00A8FC,
		Author: &discordgo.MessageEmbedAuthor{
			Name: company,
		},
		Fields: fields,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Sunfire Remote Career Matcher • Profile: Daniel Kill",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}
}

func checkJobUpdates(s *discordgo.Session, cfg *Config) {
	if cfg == nil {
		return
	}

	gCfg := getGuildSettings(SunfireServerID)
	if !gCfg.JobsEnabled || gCfg.JobsChannelID == "" {
		return
	}

	client := notifierClient(35 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var urls []string
	if len(gCfg.JobsURLs) > 0 {
		urls = gCfg.JobsURLs
	} else if cfg != nil && len(cfg.JobsURLs) > 0 {
		urls = cfg.JobsURLs
	}

	candidates := fetchAllCandidateJobs(ctx, client, urls)
	if len(candidates) == 0 {
		log.Printf("Job notifier: no candidate jobs retrieved")
		return
	}

	// Score all candidate jobs
	var matchedJobs []JobPosting
	for _, job := range candidates {
		score, highlights, ok := evaluateJobMatch(job)
		if ok {
			job.Score = score
			job.Highlights = highlights
			matchedJobs = append(matchedJobs, job)
		}
	}

	// Sort highest scoring jobs first
	sort.Slice(matchedJobs, func(i, j int) bool {
		return matchedJobs[i].Score > matchedJobs[j].Score
	})

	jobCacheMu.Lock()
	defer jobCacheMu.Unlock()

	// Initial seed handling:
	// If uninitialized, seed all seen IDs, but post the top 3 best matching jobs right now
	// so the user receives immediate confirmation of active matches.
	if !jobCache.Initialized {
		log.Printf("Job notifier: first run initializing cache (%d candidates, %d matches)", len(candidates), len(matchedJobs))
		for _, j := range candidates {
			jobCache.SeenJobs[j.ID] = time.Now().Format(time.RFC3339)
		}

		// Post top 3 matches as welcome batch
		topMatches := matchedJobs
		if len(topMatches) > 3 {
			topMatches = topMatches[:3]
		}

		header := "🔔 **Remote Job Matcher Initialized** — Top Current Career Matches for Daniel Kill:\n"
		if gCfg.JobsRoleID != nil && *gCfg.JobsRoleID != "" {
			header = fmt.Sprintf("<@&%s> %s", *gCfg.JobsRoleID, header)
		}

		for idx, match := range topMatches {
			msgSend := &discordgo.MessageSend{
				Content: "",
			}
			if idx == 0 {
				msgSend.Content = header
			}
			msgSend.Embeds = []*discordgo.MessageEmbed{buildJobEmbed(match)}

			if _, err := sendNotifierComplex(s, gCfg.JobsChannelID, msgSend); err != nil {
				log.Printf("Job notifier: failed posting initial job %s: %v", match.ID, err)
			} else {
				log.Printf("Job notifier: posted initial job %q (%s) to %s", match.Title, match.ID, gCfg.JobsChannelID)
			}
			time.Sleep(1500 * time.Millisecond)
		}

		jobCache.Initialized = true
		if err := saveJobsCache(); err != nil {
			log.Printf("Job notifier: save initial cache: %v", err)
		}
		return
	}

	// Routine daily check: post newly found matching jobs
	newMatchesCount := 0
	for _, match := range matchedJobs {
		if _, seen := jobCache.SeenJobs[match.ID]; seen {
			continue
		}

		header := ""
		if gCfg.JobsRoleID != nil && *gCfg.JobsRoleID != "" {
			header = fmt.Sprintf("<@&%s> ", *gCfg.JobsRoleID)
		}

		msgSend := &discordgo.MessageSend{
			Content: header,
			Embeds:  []*discordgo.MessageEmbed{buildJobEmbed(match)},
		}

		if _, err := sendNotifierComplex(s, gCfg.JobsChannelID, msgSend); err != nil {
			log.Printf("Job notifier: failed posting job %s: %v", match.ID, err)
			continue
		}

		log.Printf("Job notifier: posted %q (%s, score %d) to %s", match.Title, match.ID, match.Score, gCfg.JobsChannelID)
		jobCache.SeenJobs[match.ID] = time.Now().Format(time.RFC3339)
		newMatchesCount++

		time.Sleep(1500 * time.Millisecond)
	}

	// Prune old entries if cache grows past max limit
	if len(jobCache.SeenJobs) > maxCachedJobs {
		type kv struct {
			key string
			val string
		}
		var list []kv
		for k, v := range jobCache.SeenJobs {
			list = append(list, kv{k, v})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].val > list[j].val // most recent first
		})
		jobCache.SeenJobs = make(map[string]string)
		for i := 0; i < maxCachedJobs/2 && i < len(list); i++ {
			jobCache.SeenJobs[list[i].key] = list[i].val
		}
	}

	if newMatchesCount > 0 {
		if err := saveJobsCache(); err != nil {
			log.Printf("Job notifier: save cache: %v", err)
		}
	}
}

func startJobsNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initJobsCache()
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Job notifier: load config: %v", err)
		return
	}

	interval := defaultJobsInterval
	if cfg != nil && cfg.JobsPollIntervalMs > 0 {
		interval = time.Duration(cfg.JobsPollIntervalMs) * time.Millisecond
	} else if cfg != nil && cfg.Guild.JobsIntervalHours > 0 {
		interval = time.Duration(cfg.Guild.JobsIntervalHours) * time.Hour
	}

	log.Printf("Remote jobs notifier daemon started (interval %v)", interval)
	// Initial delay 25s, then periodic interval
	runPeriodic(ctx, 25*time.Second, interval, func() { checkJobUpdates(s, cfg) })
}
