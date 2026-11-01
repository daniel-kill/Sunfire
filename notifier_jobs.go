package main

import (
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

// isUSRemoteEligible checks if a location is eligible for a US-based remote candidate.
func isUSRemoteEligible(loc string) bool {
	l := strings.ToLower(strings.TrimSpace(loc))
	if l == "" || l == "remote" || l == "anywhere" || l == "worldwide" || l == "global" {
		return true
	}
	if strings.Contains(l, "us") || strings.Contains(l, "usa") || strings.Contains(l, "united states") || strings.Contains(l, "north america") {
		return true
	}

	// Exclusive foreign locations that explicitly bar US candidates
	foreignOnly := []string{
		"uk only", "united kingdom only", "europe only", "eu only", "emea only",
		"germany only", "canada only", "latam only", "apac only", "asia only",
		"australia only", "india only", "philippines only", "brazil only",
		"singapore only", "spain only", "france only", "poland only",
	}
	for _, f := range foreignOnly {
		if strings.Contains(l, f) {
			return false
		}
	}

	// If it doesn't forbid US and has generic remote indicators
	return true
}

// evaluateJobMatch scores a job against Daniel Kill's resume and qualification profile.
func evaluateJobMatch(job JobPosting) (score int, highlights []string, matches bool) {
	titleLower := strings.ToLower(job.Title)
	descLower := strings.ToLower(job.Description)
	tagsLower := strings.ToLower(strings.Join(job.Tags, " "))
	fullText := titleLower + " " + descLower + " " + tagsLower

	// Location check: must be US remote compatible
	if !isUSRemoteEligible(job.Location) {
		return 0, nil, false
	}

	// Disqualifiers: intern, junior, or strictly non-technical roles
	disqualifiers := []string{
		"intern", "internship", "junior developer", "junior engineer",
		"account executive", "sales development", "customer service rep",
		"copywriter", "graphic designer", "paralegal", "receptionist",
	}
	for _, dq := range disqualifiers {
		if strings.Contains(titleLower, dq) {
			return 0, nil, false
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
		strings.Contains(titleLower, "head of infrastructure") {
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
		strings.Contains(titleLower, "cloud architect") {
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
// Core Matcher & Dispatcher
// ----------------------------------------------------------------------------

func fetchAllCandidateJobs(ctx context.Context, client *http.Client) []JobPosting {
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

	wg.Wait()

	// Deduplicate candidates across feeds and categories by Job ID
	seen := make(map[string]bool)
	var deduped []JobPosting
	for _, j := range allJobs {
		if j.ID == "" || seen[j.ID] {
			continue
		}
		seen[j.ID] = true
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

	fields := []*discordgo.MessageEmbedField{
		{
			Name:   "🌐 Remote Status",
			Value:  fmt.Sprintf("100%% Remote (%s)", job.Location),
			Inline: true,
		},
		{
			Name:   "💰 Compensation",
			Value:  job.Salary,
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

	candidates := fetchAllCandidateJobs(ctx, client)
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
