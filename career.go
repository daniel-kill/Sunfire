// career.go — Daniel Kill's Career & Job Search Copilot for Sunfire.
// Provides application tracking, 1-click alert integration, AI fit analysis,
// cover letter drafting, certification roadmaps, and daily career briefings.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// JobApplication represents a tracked job application.
type JobApplication struct {
	ID           string    `json:"id"`
	Organization string    `json:"organization"`
	Position     string    `json:"position"`
	URL          string    `json:"url,omitempty"`
	DateApplied  time.Time `json:"dateApplied"`
	Status       string    `json:"status"` // "Applied", "Saved", "Interviewing", "Offer", "Rejected", "Archived"
	Notes        string    `json:"notes,omitempty"`
	FollowUpDate time.Time `json:"followUpDate"`
	FollowedUp   bool      `json:"followedUp"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// CareerActionItem represents administrative or qualification checklist items (e.g., from Jobs.txt).
type CareerActionItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Category  string    `json:"category"` // "Education", "Certification", "Admin", "General"
	Status    string    `json:"status"`   // "Pending", "Completed"
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// CertGoal represents items from the Certificate Planning roadmap.
type CertGoal struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Program   string `json:"program"`
	Focus     string `json:"focus"`
	URL       string `json:"url"`
	Priority  string `json:"priority"` // "High", "Medium", "Low"
	Status    string `json:"status"`   // "Not Started", "In Progress", "Completed"
	Relevance string `json:"relevance"`
}

// CareerStore holds all career-related state.
type CareerStore struct {
	Applications []JobApplication   `json:"applications"`
	ActionItems  []CareerActionItem `json:"actionItems"`
	CertGoals    []CertGoal         `json:"certGoals"`
	LastBriefing string             `json:"lastBriefing,omitempty"` // YYYY-MM-DD
}

var (
	careerMu          sync.Mutex
	careerStore       CareerStore
	careerRecentJobs  = make(map[string]JobPosting) // hash -> JobPosting for component buttons
	careerStoreLoaded bool
)

// careerDataPath returns the path to career_store.json in .private/career.
func careerDataPath() string {
	return applicationPath(".private", "career", "career_store.json")
}

// resumeMarkdownPath returns the path to Resume.md.
func resumeMarkdownPath() string {
	return applicationPath(".private", "career", "Resume.md")
}

// shortJobHash returns a deterministic 12-char hash for Discord custom IDs.
func shortJobHash(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:12]
}

// cacheRecentJob keeps a job in memory so component buttons can retrieve full context.
func cacheRecentJob(job JobPosting) string {
	careerMu.Lock()
	defer careerMu.Unlock()
	h := shortJobHash(job.ID)
	if h == "" {
		h = shortJobHash(job.Title + job.Company)
	}
	careerRecentJobs[h] = job

	// Keep memory bounded
	if len(careerRecentJobs) > 300 {
		for k := range careerRecentJobs {
			delete(careerRecentJobs, k)
			if len(careerRecentJobs) <= 200 {
				break
			}
		}
	}
	return h
}

// getRecentJob looks up a job posting by its short hash.
func getRecentJob(hash string) (JobPosting, bool) {
	careerMu.Lock()
	defer careerMu.Unlock()
	j, ok := careerRecentJobs[hash]
	return j, ok
}

// initCareerStore loads career_store.json or seeds it from initial career data.
func initCareerStore() {
	careerMu.Lock()
	defer careerMu.Unlock()
	if careerStoreLoaded {
		return
	}

	p := careerDataPath()
	data, err := os.ReadFile(p)
	if err == nil {
		if err := json.Unmarshal(data, &careerStore); err == nil {
			careerStoreLoaded = true
			return
		}
		log.Printf("career: error parsing %s: %v", p, err)
	}

	// Seed initial data if store does not exist
	careerStore = CareerStore{
		Applications: []JobApplication{
			{
				ID:           "app-cvs-health",
				Organization: "CVS Health",
				Position:     "Call Center Service Advocate I - Remote",
				URL:          "https://jobs.cvshealth.com/",
				DateApplied:  time.Date(2026, 11, 6, 0, 0, 0, 0, time.Local),
				Status:       "Applied",
				Notes:        "Applied via online portal on Nov 6, 2026",
				FollowUpDate: time.Date(2026, 11, 13, 0, 0, 0, 0, time.Local),
				FollowedUp:   false,
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
			},
			{
				ID:           "app-hoopla",
				Organization: "Hoopla",
				Position:     "Customer Support Specialist",
				URL:          "https://www.hoopladigital.com/",
				DateApplied:  time.Date(2026, 11, 6, 0, 0, 0, 0, time.Local),
				Status:       "Applied",
				Notes:        "Applied via company careers page on Nov 6, 2026",
				FollowUpDate: time.Date(2026, 11, 13, 0, 0, 0, 0, time.Local),
				FollowedUp:   false,
				CreatedAt:    time.Now(),
				UpdatedAt:    time.Now(),
			},
		},
		ActionItems: []CareerActionItem{
			{
				ID:        "todo-owens-gpa",
				Title:     "Get college GPA from Owens Community College",
				Category:  "Education",
				Status:    "Pending",
				CreatedAt: time.Now(),
			},
			{
				ID:        "todo-penta-gpa",
				Title:     "Get high school GPA from Penta Career Center",
				Category:  "Education",
				Status:    "Pending",
				CreatedAt: time.Now(),
			},
			{
				ID:        "todo-ood-cert",
				Title:     "Ask about certification assistance and funding from OOD",
				Category:  "Certification",
				Status:    "Pending",
				Notes:     "Opportunities for Ohioans with Disabilities",
				CreatedAt: time.Now(),
			},
		},
		CertGoals: []CertGoal{
			{
				ID:        "cert-msft",
				Provider:  "Microsoft",
				Program:   "Microsoft Learn",
				Focus:     "PowerShell, AZ-900, SC-900, Azure AI",
				URL:       "https://learn.microsoft.com/en-us/training/",
				Priority:  "High",
				Status:    "Not Started",
				Relevance: "Extends 22+ years Windows Server, Hyper-V, and PowerShell 7 experience directly into cloud Azure governance.",
			},
			{
				ID:        "cert-ibm",
				Provider:  "IBM",
				Program:   "IBM SkillsBuild",
				Focus:     "AI Fundamentals, Cybersecurity, Data",
				URL:       "https://skillsbuild.org/",
				Priority:  "High",
				Status:    "Not Started",
				Relevance: "Accessible remote credentials validating modern AI engineering and security compliance.",
			},
			{
				ID:        "cert-aws",
				Provider:  "Amazon / AWS",
				Program:   "AWS Skill Builder",
				Focus:     "Cloud Practitioner, Gen AI, Security",
				URL:       "https://skillbuilder.aws/",
				Priority:  "Medium",
				Status:    "Not Started",
				Relevance: "Complements Docker containerization and VPS/cloud deployment skills.",
			},
			{
				ID:        "cert-nvidia",
				Provider:  "NVIDIA",
				Program:   "Deep Learning Institute",
				Focus:     "Fundamentals of Deep Learning, Gen AI",
				URL:       "https://www.nvidia.com/en-us/training/self-paced-courses/",
				Priority:  "Medium",
				Status:    "Not Started",
				Relevance: "Deepens mechanics of quantized local LLM execution on consumer/mobile GPUs.",
			},
			{
				ID:        "cert-google",
				Provider:  "Google",
				Program:   "Skillshop / Cloud",
				Focus:     "Analytics, Cloud & Infrastructure",
				URL:       "https://skillshop.withgoogle.com/",
				Priority:  "Medium",
				Status:    "Not Started",
				Relevance: "Broadens multi-cloud monitoring and metrics understanding.",
			},
		},
	}
	careerStoreLoaded = true
	_ = saveCareerStoreLocked()
}

// saveCareerStoreLocked saves the store to disk while careerMu is held.
func saveCareerStoreLocked() error {
	p := careerDataPath()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(careerStore, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0644)
}

// buildJobActionRow constructs the interactive 1-click buttons for scraped job alert cards.
func buildJobActionRow(job JobPosting) []discordgo.MessageComponent {
	hash := cacheRecentJob(job)
	var buttons []discordgo.MessageComponent

	buttons = append(buttons, discordgo.Button{
		Label:    "Apply / Save",
		Style:    discordgo.SuccessButton,
		CustomID: "job:track:" + hash,
		Emoji:    &discordgo.ComponentEmoji{Name: "📌"},
	})
	buttons = append(buttons, discordgo.Button{
		Label:    "AI Fit Breakdown",
		Style:    discordgo.PrimaryButton,
		CustomID: "job:fit:" + hash,
		Emoji:    &discordgo.ComponentEmoji{Name: "🔍"},
	})
	if job.URL != "" && strings.HasPrefix(job.URL, "http") {
		buttons = append(buttons, discordgo.Button{
			Label: "View Listing",
			Style: discordgo.LinkButton,
			URL:   job.URL,
			Emoji: &discordgo.ComponentEmoji{Name: "🔗"},
		})
	}

	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: buttons,
		},
	}
}

// handleJobComponent handles button clicks on job alert cards (job:track:* and job:fit:*).
func handleJobComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initCareerStore()
	data := i.MessageComponentData()
	customID := data.CustomID

	if strings.HasPrefix(customID, "job:track:") {
		hash := strings.TrimPrefix(customID, "job:track:")
		job, ok := getRecentJob(hash)

		// Fallback to reading from the message embed if recent job memory was cleared
		if !ok && i.Message != nil && len(i.Message.Embeds) > 0 {
			emb := i.Message.Embeds[0]
			job.Title = emb.Title
			job.URL = emb.URL
			if emb.Author != nil {
				job.Company = emb.Author.Name
			}
			job.ID = hash
		}

		if job.Title == "" && job.Company == "" {
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Could not retrieve details for this job alert. Try logging it via `/career apply`.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		careerMu.Lock()
		// Check for existing application by company & title
		for _, app := range careerStore.Applications {
			if strings.EqualFold(app.Organization, job.Company) && strings.EqualFold(app.Position, job.Title) {
				careerMu.Unlock()
				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: fmt.Sprintf("ℹ️ You already have **%s** at **%s** tracked in your pipeline (Status: `%s`).", app.Position, app.Organization, app.Status),
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}
		}

		now := time.Now()
		appID := fmt.Sprintf("app-%d", now.UnixNano()%1000000)
		newApp := JobApplication{
			ID:           appID,
			Organization: job.Company,
			Position:     job.Title,
			URL:          job.URL,
			DateApplied:  now,
			Status:       "Saved",
			Notes:        fmt.Sprintf("Saved from automated job alert (Score: %d/100)", job.Score),
			FollowUpDate: now.Add(7 * 24 * time.Hour),
			FollowedUp:   false,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		careerStore.Applications = append([]JobApplication{newApp}, careerStore.Applications...)
		_ = saveCareerStoreLocked()
		careerMu.Unlock()

		// Schedule a reminder in reminders system if invoker is available
		invokerID := ""
		if i.Member != nil && i.Member.User != nil {
			invokerID = i.Member.User.ID
		} else if i.User != nil {
			invokerID = i.User.ID
		}
		if invokerID != "" && i.ChannelID != "" {
			scheduleCareerFollowUp(i.ChannelID, invokerID, job.Company, job.Title, now.Add(7*24*time.Hour))
		}

		msg := fmt.Sprintf("✅ **Saved to Career Pipeline!**\n\n📌 **Role:** %s\n🏢 **Company:** %s\n⏳ **Status:** Saved / Queued\n🔔 **Follow-up Scheduled:** %s (in 7 days)\n\nUse `/career status` to manage your active applications.",
			job.Title, job.Company, newApp.FollowUpDate.Format("Mon, Jan 02"))

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: msg,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if strings.HasPrefix(customID, "job:fit:") {
		// Defer interaction response while generating AI breakdown
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags: discordgo.MessageFlagsEphemeral,
			},
		})

		hash := strings.TrimPrefix(customID, "job:fit:")
		job, ok := getRecentJob(hash)
		if !ok && i.Message != nil && len(i.Message.Embeds) > 0 {
			emb := i.Message.Embeds[0]
			job.Title = emb.Title
			job.URL = emb.URL
			if emb.Author != nil {
				job.Company = emb.Author.Name
			}
			job.Description = emb.Description
		}

		analysis, err := performAIFitAnalysis(context.Background(), job.Title, job.Company, job.Description, job.URL)
		if err != nil {
			followUpEphemeral(s, i, "❌ Analysis error: "+err.Error())
			return
		}

		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("🔍 AI Fit & ATS Breakdown: %s", job.Title),
			Description: fmt.Sprintf("**Target Employer:** %s\n\n%s", job.Company, analysis),
			Color:       0x1e3a8a, // Sunfire Navy
			Footer: &discordgo.MessageEmbedFooter{
				Text: "Sunfire Career Copilot • Evaluated against Resume.md",
			},
			Timestamp: time.Now().Format(time.RFC3339),
		}

		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		})
		return
	}
}

// scheduleCareerFollowUp adds a reminder to Sunfire's native reminders system.
func scheduleCareerFollowUp(channelID, userID, company, position string, fireTime time.Time) {
	remindersMu.Lock()
	defer remindersMu.Unlock()
	_ = loadReminders()
	reminders = append(reminders, Reminder{
		ID:        newReminderID(reminders, fireTime),
		UserID:    userID,
		ChannelID: channelID,
		Message:   fmt.Sprintf("🔔 **Career Follow-Up Due:** It's been 7 days since you saved/applied for **%s** at **%s**. Have you followed up or heard from the hiring team?", position, company),
		FireTime:  fireTime,
	})
	_ = saveReminders()
}

// performAIFitAnalysis compares job details with Resume.md using Sunfire's LLM routing.
func performAIFitAnalysis(ctx context.Context, title, company, description, url string) (string, error) {
	resumeBytes, err := os.ReadFile(resumeMarkdownPath())
	resumeText := string(resumeBytes)
	if err != nil || len(resumeText) == 0 {
		resumeText = "Daniel Kill — Senior IT Manager & Infrastructure Engineer (22+ years enterprise IT leadership, Linux/Debian, Go, Docker, PowerShell 7, Hyper-V, multi-provider AI)."
	}

	jobCtx := fmt.Sprintf("Role: %s\nCompany: %s\nDetails: %s\nLink: %s", title, company, description, url)
	prompt := fmt.Sprintf(`You are an expert executive tech recruiter and ATS optimization specialist advising Daniel Kill.
Analyze the following job opportunity against Daniel Kill's authoritative resume.

Target Job:
%s

Daniel Kill's Resume:
%s

Provide a concise, punchy evaluation with these exact markdown sections:
### 1. Key Matching Strengths
(Highlight 2-3 specific achievements or skills from his 22-year IT Manager tenure or modern Go/DevOps stack that directly qualify him)

### 2. High-Impact ATS Keywords
(List 6-8 crucial keywords and phrases from the job description to weave into his application)

### 3. Strategic Interview Angle
(Give 1-2 tactical talking points explaining why he is the ideal remote senior hire for this specific team)`, jobCtx, resumeText)

	res, err := callCareerLLM(ctx, "You are a career strategist specializing in senior IT leadership and DevOps roles.", prompt)
	if err != nil {
		// Deterministic fallback if API offline
		return generateDeterministicMatch(title, company, description), nil
	}
	return res, nil
}

// generateDeterministicMatch provides an instant, offline-safe fit analysis based on Resume.md.
func generateDeterministicMatch(title, company, description string) string {
	var strengths []string
	var keywords []string

	tLower := strings.ToLower(title + " " + description)
	if strings.Contains(tLower, "manager") || strings.Contains(tLower, "lead") || strings.Contains(tLower, "director") {
		strengths = append(strengths, "• **22+ Years Enterprise IT Leadership:** Multi-facility IT operations management at Bohl Equipment Co., directing infrastructure strategy, DR/BC, policy governance, and vendor negotiations.")
		keywords = append(keywords, "IT Operations", "Infrastructure Modernization", "Disaster Recovery", "Vendor Management")
	}
	if strings.Contains(tLower, "devops") || strings.Contains(tLower, "docker") || strings.Contains(tLower, "linux") || strings.Contains(tLower, "cloud") {
		strengths = append(strengths, "• **Modern DevOps & Automation:** Production Go discord platform engineering, automated PowerShell 7 / rsync Linux watchers, Docker container transcoding, and cloud VPS administration.")
		keywords = append(keywords, "Docker", "PowerShell 7", "Linux (Ubuntu/Debian)", "CI/CD Pipelines", "rsync synchronization")
	}
	if strings.Contains(tLower, "support") || strings.Contains(tLower, "systems") || strings.Contains(tLower, "network") {
		strengths = append(strengths, "• **Mission-Critical Systems Reliability:** Maintained 99.9% uptime across multi-server Hyper-V virtualization clusters, Windows Server domain controllers, network routing, and ERP systems.")
		keywords = append(keywords, "Hyper-V Virtualization", "Network Administration", "System Reliability (99.9% Uptime)", "Technical Support Leadership")
	}

	if len(strengths) == 0 {
		strengths = append(strengths, "• **Extensive Leadership & Technical Adaptability:** Over two decades combining high-level IT governance with hands-on systems architecture in Go, Linux, Docker, and hybrid Windows/WSL2 environments.")
		keywords = append(keywords, "IT Leadership", "Systems Architecture", "Infrastructure Engineering", "Security & Compliance")
	}

	return fmt.Sprintf("### 1. Key Matching Strengths\n%s\n\n### 2. High-Impact ATS Keywords\n`%s`\n\n### 3. Strategic Interview Angle\nFocus on your proven ability to bridge the gap between high-level operational leadership and modern hands-on technical execution (Go, Linux, containerization, and 99.9%% reliability).",
		strings.Join(strengths, "\n"), strings.Join(keywords, "`, `"))
}

// generateTailoredCoverLetter drafts a customized cover letter from Resume.md.
func generateTailoredCoverLetter(ctx context.Context, company, position, description, tone string) (string, error) {
	resumeBytes, err := os.ReadFile(resumeMarkdownPath())
	resumeText := string(resumeBytes)
	if err != nil || len(resumeText) == 0 {
		resumeText = "Daniel Kill — Senior IT Manager & Infrastructure Engineer (22+ years enterprise IT leadership, Linux/Debian, Go, Docker, PowerShell 7, Hyper-V, multi-provider AI)."
	}

	if tone == "" {
		tone = "Balanced (Executive Leadership + Modern Engineering)"
	}

	prompt := fmt.Sprintf(`Write a professional, compelling, 3-to-4 paragraph cover letter for Daniel Kill applying for the position of "%s" at "%s".

Target Details / Requirements:
%s

Tone: %s
Authoritative Background (Resume.md):
%s

Guidelines:
1. Emphasize his 22+ years of senior IT leadership at Bohl Equipment Co. (multi-facility operations, 99.9%% uptime, major migrations, DR/BC, executive reporting).
2. Pair this leadership with his modern hands-on software & systems architecture expertise (Go/Golang discord platforms, Linux/Debian daemons, Docker container pipelines, PowerShell 7 automation, multi-provider AI routing).
3. Emphasize his dedication to strictly 100%% remote execution and high accountability.
4. Keep it direct, punchy, confident, and ATS-optimized. Do NOT use generic fluff or placeholder brackets.`, position, company, description, tone, resumeText)

	res, err := callCareerLLM(ctx, "You are an executive cover letter drafter creating high-converting application letters for tech leaders.", prompt)
	if err != nil {
		// High-quality deterministic fallback
		return generateDeterministicCoverLetter(company, position), nil
	}
	return res, nil
}

// generateDeterministicCoverLetter generates a clean, well-formatted letter when API is offline.
func generateDeterministicCoverLetter(company, position string) string {
	return fmt.Sprintf(`Dear Hiring Team at %s,

I am writing to express my enthusiastic interest in the %s position. With over 22 years of enterprise technology leadership, infrastructure modernization, and systems administration experience, I offer a proven track record of directing complex IT operations, enforcing cybersecurity governance, and ensuring 99.9%% uptime for mission-critical business systems.

Throughout my 22-year tenure as IT Manager for Bohl Equipment Company, I served as the primary technology leader reporting directly to executive management. I architected and executed comprehensive infrastructure migrations that increased operational efficiency by 30%%, managed multi-server Hyper-V virtualization clusters, and authored rigorous data protection and disaster recovery procedures. Crucially, I pair this strategic executive background with deep, hands-on engineering expertise in modern technologies—including Go (Golang), Linux/Debian systemd daemons, Docker containerization, PowerShell 7 automation, and multi-provider AI systems architecture.

Operating as an independent systems architect since 2023, I have engineered enterprise-grade notification platforms and automated hybrid DevOps synchronization pipelines maintaining real-time byte parity between workstations and cloud VPS environments. My experience has proven that high-performing remote operations require uncompromising accountability, proactive communication, and resilient architecture.

I would welcome the opportunity to discuss how my dual strengths in strategic IT leadership and modern systems engineering can drive immediate value for %s. Thank you for your time and consideration.

Sincerely,
Daniel Kill
(567) 249-2185 | yourwishismine@gmail.com
Walbridge, OH (Strictly 100%% Remote)
Portfolio: https://danielkill.online | GitHub: https://github.com/daniel-kill`, company, position, company)
}

// callCareerLLM attempts OpenRouter first, then Venice / fallback.
func callCareerLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if orAPIKey() != "" {
		payload := map[string]any{
			"model": orDefaultAsk,
			"messages": []map[string]string{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": userPrompt},
			},
			"max_tokens":   1600,
			"temperature":  0.4,
		}
		bodyBytes, err := json.Marshal(payload)
		if err == nil {
			req, err := orRequest(http.MethodPost, "/chat/completions", nil, bodyBytes)
			if err == nil {
				req = req.WithContext(ctx)
				body, status, err := orDo(req)
				if err == nil && status == http.StatusOK {
					var res struct {
						Choices []struct {
							Message struct {
								Content string `json:"content"`
							} `json:"message"`
						} `json:"choices"`
					}
					if json.Unmarshal(body, &res) == nil && len(res.Choices) > 0 && res.Choices[0].Message.Content != "" {
						return strings.TrimSpace(res.Choices[0].Message.Content), nil
					}
				}
			}
		}
	}
	return "", errors.New("external LLM provider currently unreachable")
}

// careerCommandSpec registers the /career slash command in Sunfire.
func careerCommandSpec() commandSpec {
	return commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "career",
			Description: "Daniel Kill's Career & Job Search Copilot",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "apply",
					Description: "Log a submitted application and schedule follow-up",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "company", Description: "Target employer / organization", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "position", Description: "Job title / position applied for", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "Job listing or application portal URL", Required: false},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "status",
							Description: "Initial application status (default: Applied)",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Applied", Value: "Applied"},
								{Name: "Saved", Value: "Saved"},
								{Name: "Interviewing", Value: "Interviewing"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionString, Name: "notes", Description: "Recruiter contacts, salary, interview notes", Required: false},
						{Type: discordgo.ApplicationCommandOptionInteger, Name: "followup_days", Description: "Days until follow-up alert (default: 7)", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "status",
					Description: "View active applications, pipeline summary, and follow-ups due",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "filter",
							Description: "Filter status view",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Active (Applied / Interviewing / Saved)", Value: "Active"},
								{Name: "Follow-ups Due Today", Value: "Due"},
								{Name: "All Applications", Value: "All"},
								{Name: "Interviewing Only", Value: "Interviewing"},
								{Name: "Applied Only", Value: "Applied"},
								{Name: "Saved Only", Value: "Saved"},
							},
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "update",
					Description: "Update status or add notes to an application",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Application ID or company name substring", Required: true},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "status",
							Description: "New status",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Interviewing", Value: "Interviewing"},
								{Name: "Applied", Value: "Applied"},
								{Name: "Offer", Value: "Offer"},
								{Name: "Rejected", Value: "Rejected"},
								{Name: "Archived", Value: "Archived"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionString, Name: "notes", Description: "Add or update notes", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "match",
					Description: "Analyze job fit and ATS keyword alignment against Resume.md",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "Job posting link to scrape and evaluate", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "description", Description: "Pasted job description text or requirements", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "coverletter",
					Description: "Generate an AI-tailored cover letter from Resume.md",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "company", Description: "Target company name", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "position", Description: "Target job title", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "description", Description: "Job description or core requirements", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "Job posting URL", Required: false},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "tone",
							Description: "Tone of the letter",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Balanced (Leadership + Modern Engineering)", Value: "Balanced"},
								{Name: "Executive Leadership Focus", Value: "Executive Leadership Focus"},
								{Name: "Technical DevOps & Systems Focus", Value: "Technical DevOps & Systems Focus"},
							},
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "todos",
					Description: "View and manage career checklist action items (Jobs.txt)",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "action",
							Description: "Action to perform",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "View Todos", Value: "view"},
								{Name: "Complete Item", Value: "complete"},
								{Name: "Add New Item", Value: "add"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionString, Name: "item", Description: "Item ID or title to complete/add", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "certs",
					Description: "View certification roadmap & study links (Certificate Planning)",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "standup",
					Description: "Generate on-demand Morning Career Briefing standup",
				},
			},
		},
		Handler: handleCareerCommand,
	}
}

// handleCareerCommand routes /career subcommands.
func handleCareerCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initCareerStore()
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		showCareerHelp(s, i)
		return
	}

	sub := data.Options[0]
	switch sub.Name {
	case "apply":
		handleCareerApply(s, i, sub)
	case "status":
		handleCareerStatus(s, i, sub)
	case "update":
		handleCareerUpdate(s, i, sub)
	case "match":
		handleCareerMatch(s, i, sub)
	case "coverletter":
		handleCareerCoverLetter(s, i, sub)
	case "todos":
		handleCareerTodos(s, i, sub)
	case "certs":
		handleCareerCerts(s, i, sub)
	case "standup":
		handleCareerStandup(s, i)
	default:
		showCareerHelp(s, i)
	}
}

func showCareerHelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := &discordgo.MessageEmbed{
		Title:       "🚀 Sunfire Career & Job Search Copilot",
		Description: "Your personal autonomous job-hunt copilot backed by `.private/career` and `Resume.md`.",
		Color:       0x1e3a8a,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "`/career apply`", Value: "Log an application, set follow-up alerts, and track status.", Inline: false},
			{Name: "`/career status`", Value: "View your pipeline dashboard and follow-ups due.", Inline: false},
			{Name: "`/career match`", Value: "Run AI fit analysis and ATS keyword breakdown for any job.", Inline: false},
			{Name: "`/career coverletter`", Value: "Generate a custom, tailored cover letter from Resume.md.", Inline: false},
			{Name: "`/career todos`", Value: "View/complete action items (Owens GPA, Penta GPA, OOD assistance).", Inline: false},
			{Name: "`/career certs`", Value: "View your Big Tech certification roadmap (AZ-900, IBM SkillsBuild).", Inline: false},
			{Name: "`/career standup`", Value: "Post the morning career briefing standup on demand.", Inline: false},
		},
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

func handleCareerApply(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	var company, position, url, status, notes string
	followUpDays := 7

	for _, opt := range sub.Options {
		switch opt.Name {
		case "company":
			company = strings.TrimSpace(opt.StringValue())
		case "position":
			position = strings.TrimSpace(opt.StringValue())
		case "url":
			url = strings.TrimSpace(opt.StringValue())
		case "status":
			status = strings.TrimSpace(opt.StringValue())
		case "notes":
			notes = strings.TrimSpace(opt.StringValue())
		case "followup_days":
			if opt.IntValue() > 0 {
				followUpDays = int(opt.IntValue())
			}
		}
	}

	if status == "" {
		status = "Applied"
	}

	careerMu.Lock()
	now := time.Now()
	appID := fmt.Sprintf("app-%d", now.UnixNano()%1000000)
	newApp := JobApplication{
		ID:           appID,
		Organization: company,
		Position:     position,
		URL:          url,
		DateApplied:  now,
		Status:       status,
		Notes:        notes,
		FollowUpDate: now.Add(time.Duration(followUpDays) * 24 * time.Hour),
		FollowedUp:   false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	careerStore.Applications = append([]JobApplication{newApp}, careerStore.Applications...)
	_ = saveCareerStoreLocked()
	careerMu.Unlock()

	invokerID := ""
	if i.Member != nil && i.Member.User != nil {
		invokerID = i.Member.User.ID
	} else if i.User != nil {
		invokerID = i.User.ID
	}
	if invokerID != "" && i.ChannelID != "" {
		scheduleCareerFollowUp(i.ChannelID, invokerID, company, position, newApp.FollowUpDate)
	}

	embed := &discordgo.MessageEmbed{
		Title:       "✅ Application Successfully Logged",
		Description: fmt.Sprintf("Logged **%s** for **%s** into your pipeline.", position, company),
		Color:       0x10b981, // Emerald Green
		Fields: []*discordgo.MessageEmbedField{
			{Name: "🏢 Company", Value: company, Inline: true},
			{Name: "🎯 Position", Value: position, Inline: true},
			{Name: "⏳ Status", Value: status, Inline: true},
			{Name: "🔔 Follow-up Due", Value: fmt.Sprintf("%s (in %d days)", newApp.FollowUpDate.Format("Mon, Jan 02"), followUpDays), Inline: true},
			{Name: "🆔 App ID", Value: fmt.Sprintf("`%s`", appID), Inline: true},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Use /career update to modify notes or status as interviews progress.",
		},
		Timestamp: now.Format(time.RFC3339),
	}
	if url != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "🔗 Link", Value: url, Inline: false})
	}
	if notes != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "📝 Notes", Value: notes, Inline: false})
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

func handleCareerStatus(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	filter := "Active"
	for _, opt := range sub.Options {
		if opt.Name == "filter" {
			filter = opt.StringValue()
		}
	}

	careerMu.Lock()
	apps := make([]JobApplication, len(careerStore.Applications))
	copy(apps, careerStore.Applications)
	careerMu.Unlock()

	now := time.Now()
	var displayed []JobApplication
	totalApplied := 0
	totalInterviewing := 0
	totalSaved := 0
	totalDue := 0

	for _, a := range apps {
		if a.Status == "Applied" {
			totalApplied++
		} else if a.Status == "Interviewing" {
			totalInterviewing++
		} else if a.Status == "Saved" {
			totalSaved++
		}
		if now.After(a.FollowUpDate) && a.Status != "Rejected" && a.Status != "Archived" {
			totalDue++
		}

		match := false
		switch filter {
		case "All":
			match = true
		case "Due":
			match = now.After(a.FollowUpDate) && a.Status != "Rejected" && a.Status != "Archived"
		case "Applied":
			match = a.Status == "Applied"
		case "Interviewing":
			match = a.Status == "Interviewing"
		case "Saved":
			match = a.Status == "Saved"
		default: // "Active"
			match = a.Status == "Applied" || a.Status == "Interviewing" || a.Status == "Saved"
		}

		if match {
			displayed = append(displayed, a)
		}
	}

	sort.Slice(displayed, func(i, j int) bool {
		return displayed[i].DateApplied.After(displayed[j].DateApplied)
	})

	var lines []string
	for idx, a := range displayed {
		if idx >= 15 {
			lines = append(lines, fmt.Sprintf("*(...and %d more applications)*", len(displayed)-15))
			break
		}
		statusIcon := "⏳"
		if a.Status == "Interviewing" {
			statusIcon = "🎯"
		} else if a.Status == "Offer" {
			statusIcon = "🎉"
		} else if a.Status == "Saved" {
			statusIcon = "📌"
		} else if a.Status == "Rejected" {
			statusIcon = "❌"
		}

		dueNotice := ""
		if now.After(a.FollowUpDate) && a.Status != "Rejected" && a.Status != "Archived" {
			dueNotice = " **[FOLLOW-UP DUE!]**"
		}

		linkStr := ""
		if a.URL != "" {
			linkStr = fmt.Sprintf(" • [Link](<%s>)", a.URL)
		}

		lines = append(lines, fmt.Sprintf("%s **%s** — *%s*%s\n   └ ID: `%s` • Applied: %s • Status: `%s`%s",
			statusIcon, a.Position, a.Organization, linkStr, a.ID, a.DateApplied.Format("Jan 02"), a.Status, dueNotice))
	}

	bodyText := strings.Join(lines, "\n")
	if len(lines) == 0 {
		bodyText = "*No applications matching this filter.*"
	}

	embed := &discordgo.MessageEmbed{
		Title: fmt.Sprintf("📊 Career Applications Pipeline (%s)", filter),
		Description: fmt.Sprintf("**Summary:** %d Applied • %d Interviewing • %d Saved • 🔔 **%d Follow-ups Due**\n\n%s",
			totalApplied, totalInterviewing, totalSaved, totalDue, bodyText),
		Color: 0x1e3a8a,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Update status with /career update id: <app-id>",
		},
		Timestamp: now.Format(time.RFC3339),
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

func handleCareerUpdate(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	var targetID, newStatus, newNotes string
	for _, opt := range sub.Options {
		switch opt.Name {
		case "id":
			targetID = strings.TrimSpace(opt.StringValue())
		case "status":
			newStatus = strings.TrimSpace(opt.StringValue())
		case "notes":
			newNotes = strings.TrimSpace(opt.StringValue())
		}
	}

	careerMu.Lock()
	defer careerMu.Unlock()

	found := false
	for idx := range careerStore.Applications {
		app := &careerStore.Applications[idx]
		if strings.EqualFold(app.ID, targetID) || strings.Contains(strings.ToLower(app.Organization), strings.ToLower(targetID)) {
			if newStatus != "" {
				app.Status = newStatus
			}
			if newNotes != "" {
				if app.Notes != "" {
					app.Notes += " | " + newNotes
				} else {
					app.Notes = newNotes
				}
			}
			app.UpdatedAt = time.Now()
			found = true
			_ = saveCareerStoreLocked()

			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("✅ **Updated `%s` (%s at %s):**\n• **Status:** `%s`\n• **Notes:** %s",
						app.ID, app.Position, app.Organization, app.Status, app.Notes),
					Flags: discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}
	}

	if !found {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("❌ Could not find application with ID or Company matching `%s`.", targetID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	}
}

func handleCareerMatch(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	var jobURL, jobDesc string
	for _, opt := range sub.Options {
		switch opt.Name {
		case "url":
			jobURL = strings.TrimSpace(opt.StringValue())
		case "description":
			jobDesc = strings.TrimSpace(opt.StringValue())
		}
	}

	if jobURL == "" && jobDesc == "" {
		followUpEphemeral(s, i, "❌ Please provide either a job URL or paste description text.")
		return
	}

	title := "Custom Target Position"
	company := "Prospective Employer"
	if jobURL != "" && jobDesc == "" {
		// Scrape custom job URL using existing scrape helper
		client := notifierClient(20 * time.Second)
		scraped, err := scrapeCustomJobURL(context.Background(), client, jobURL)
		if err == nil && len(scraped) > 0 {
			title = scraped[0].Title
			company = scraped[0].Company
			jobDesc = scraped[0].Description
		}
	}

	analysis, err := performAIFitAnalysis(context.Background(), title, company, jobDesc, jobURL)
	if err != nil {
		followUpEphemeral(s, i, "❌ Analysis failed: "+err.Error())
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🔍 Resume Match & ATS Analysis: %s", title),
		Description: fmt.Sprintf("**Target Employer:** %s\n\n%s", company, analysis),
		Color:       0x1e3a8a,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Sunfire Career Copilot • Evaluated against Resume.md",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
		Flags:  discordgo.MessageFlagsEphemeral,
	})
}

func handleCareerCoverLetter(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	var company, position, jobDesc, jobURL, tone string
	for _, opt := range sub.Options {
		switch opt.Name {
		case "company":
			company = strings.TrimSpace(opt.StringValue())
		case "position":
			position = strings.TrimSpace(opt.StringValue())
		case "description":
			jobDesc = strings.TrimSpace(opt.StringValue())
		case "url":
			jobURL = strings.TrimSpace(opt.StringValue())
		case "tone":
			tone = strings.TrimSpace(opt.StringValue())
		}
	}

	if jobURL != "" && jobDesc == "" {
		client := notifierClient(20 * time.Second)
		scraped, err := scrapeCustomJobURL(context.Background(), client, jobURL)
		if err == nil && len(scraped) > 0 {
			jobDesc = scraped[0].Description
		}
	}

	letter, err := generateTailoredCoverLetter(context.Background(), company, position, jobDesc, tone)
	if err != nil {
		followUpEphemeral(s, i, "❌ Failed generating cover letter: "+err.Error())
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📄 Tailored Cover Letter: %s", position),
		Description: fmt.Sprintf("**Company:** %s • **Tone:** %s\n\n```text\n%s\n```", company, tone, letter),
		Color:       0x10b981,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Sunfire Career Copilot • Crafted from Resume.md",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	params := &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{embed},
		Flags:  discordgo.MessageFlagsEphemeral,
	}

	// Also attach as .txt file for convenience
	params.Files = []*discordgo.File{
		{
			Name:        fmt.Sprintf("Cover_Letter_%s_%s.txt", strings.ReplaceAll(company, " ", "_"), strings.ReplaceAll(position, " ", "_")),
			ContentType: "text/plain",
			Reader:      strings.NewReader(letter),
		},
	}

	_, _ = s.FollowupMessageCreate(i.Interaction, true, params)
}

func handleCareerTodos(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	action := "view"
	var itemArg string
	for _, opt := range sub.Options {
		switch opt.Name {
		case "action":
			action = opt.StringValue()
		case "item":
			itemArg = strings.TrimSpace(opt.StringValue())
		}
	}

	careerMu.Lock()
	defer careerMu.Unlock()

	if action == "complete" && itemArg != "" {
		for idx := range careerStore.ActionItems {
			todo := &careerStore.ActionItems[idx]
			if strings.EqualFold(todo.ID, itemArg) || strings.Contains(strings.ToLower(todo.Title), strings.ToLower(itemArg)) {
				todo.Status = "Completed"
				_ = saveCareerStoreLocked()
				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: fmt.Sprintf("✅ Marked item as Completed: **%s**", todo.Title),
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}
		}
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("❌ Item matching `%s` not found.", itemArg),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	if action == "add" && itemArg != "" {
		newItem := CareerActionItem{
			ID:        fmt.Sprintf("todo-%d", time.Now().UnixNano()%100000),
			Title:     itemArg,
			Category:  "General",
			Status:    "Pending",
			CreatedAt: time.Now(),
		}
		careerStore.ActionItems = append(careerStore.ActionItems, newItem)
		_ = saveCareerStoreLocked()
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Added action item: **%s** (ID: `%s`)", newItem.Title, newItem.ID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// View todos
	var lines []string
	pendingCount := 0
	for _, todo := range careerStore.ActionItems {
		icon := "⏳"
		if todo.Status == "Completed" {
			icon = "✅"
		} else {
			pendingCount++
		}
		notesStr := ""
		if todo.Notes != "" {
			notesStr = fmt.Sprintf(" *(%s)*", todo.Notes)
		}
		lines = append(lines, fmt.Sprintf("%s **%s**%s\n   └ ID: `%s` • Category: `%s` • Status: `%s`",
			icon, todo.Title, notesStr, todo.ID, todo.Category, todo.Status))
	}

	embed := &discordgo.MessageEmbed{
		Title:       "📋 Career Checklist & Action Items (Jobs.txt)",
		Description: fmt.Sprintf("**Pending Items (%d):**\n\n%s", pendingCount, strings.Join(lines, "\n")),
		Color:       0x1e3a8a,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Use /career todos action: complete item: <id> to check off an item.",
		},
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

func handleCareerCerts(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	careerMu.Lock()
	certs := make([]CertGoal, len(careerStore.CertGoals))
	copy(certs, careerStore.CertGoals)
	careerMu.Unlock()

	var fields []*discordgo.MessageEmbedField
	for _, c := range certs {
		priIcon := "⭐"
		if c.Priority == "High" {
			priIcon = "🔥 High Priority"
		} else {
			priIcon = "⚡ Medium Priority"
		}
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("%s: %s (%s)", c.Provider, c.Program, priIcon),
			Value:  fmt.Sprintf("**Focus:** %s\n**Relevance:** %s\n🔗 [Study Portal](<%s>)", c.Focus, c.Relevance, c.URL),
			Inline: false,
		})
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🎓 Big Tech Certification Roadmap",
		Description: "Personalized certification roadmap aligned with your 22+ years IT background & modern containerization/Go stack (`Certificate Planning.ods`).",
		Color:       0x1e3a8a,
		Fields:      fields,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "High-priority focus: Microsoft Learn (AZ-900 / PowerShell) & IBM SkillsBuild (AI / Security)",
		},
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

func handleCareerStandup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := buildCareerStandupEmbed()
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

// buildCareerStandupEmbed constructs the daily morning career briefing.
func buildCareerStandupEmbed() *discordgo.MessageEmbed {
	initCareerStore()
	careerMu.Lock()
	defer careerMu.Unlock()

	now := time.Now()
	var dueApps []string
	appliedCount := 0
	interviewingCount := 0
	savedCount := 0

	for _, a := range careerStore.Applications {
		if a.Status == "Applied" {
			appliedCount++
		} else if a.Status == "Interviewing" {
			interviewingCount++
		} else if a.Status == "Saved" {
			savedCount++
		}

		if now.After(a.FollowUpDate) && a.Status != "Rejected" && a.Status != "Archived" {
			dueApps = append(dueApps, fmt.Sprintf("• **%s** at **%s** (Applied %s)", a.Position, a.Organization, a.DateApplied.Format("Jan 02")))
		}
	}

	dueSection := "✅ All applications are currently up to date."
	if len(dueApps) > 0 {
		dueSection = fmt.Sprintf("🔔 **Follow-ups Due Today (%d):**\n%s", len(dueApps), strings.Join(dueApps, "\n"))
	}

	var pendingTodos []string
	for _, t := range careerStore.ActionItems {
		if t.Status == "Pending" {
			pendingTodos = append(pendingTodos, fmt.Sprintf("• %s", t.Title))
		}
	}
	todoSection := "✅ All checklist items completed."
	if len(pendingTodos) > 0 {
		todoSection = strings.Join(pendingTodos, "\n")
	}

	return &discordgo.MessageEmbed{
		Title: "🌅 Morning Career Standup & Briefing",
		Description: fmt.Sprintf("**Pipeline Status:** %d Active Applications (%d Applied, %d Interviewing, %d Saved)\n\n%s",
			appliedCount+interviewingCount+savedCount, appliedCount, interviewingCount, savedCount, dueSection),
		Color: 0x1e3a8a,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "📋 Administrative Checklist (`Jobs.txt`)",
				Value:  todoSection,
				Inline: false,
			},
			{
				Name:   "🎓 Weekly Certification Target",
				Value:  "**Microsoft Learn:** Azure Fundamentals (AZ-900) & Security (SC-900)\n• High synergy with your enterprise Windows Server & Hyper-V background.\n🔗 [Open Microsoft Learn](<https://learn.microsoft.com/en-us/training/>)",
				Inline: false,
			},
			{
				Name:   "💡 Tactical Advice for Remote Leadership Roles",
				Value:  "When interviewing, emphasize your **22-year stability and 99.9% uptime track record** alongside your modern adoption of Go, Linux, and Docker. Most IT managers cannot code; most developers cannot run infrastructure. You do both.",
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Sunfire Career Copilot • Daily Briefing",
		},
		Timestamp: now.Format(time.RFC3339),
	}
}

// startCareerBriefingDaemon checks every 30 minutes and posts the morning briefing around 9 AM.
func startCareerBriefingDaemon(ctx context.Context, s *discordgo.Session) {
	initCareerStore()
	log.Printf("Career briefing daemon started")

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			// Check if between 9:00 AM and 10:00 AM Eastern Time
			if now.Hour() == 9 {
				todayKey := now.Format("2006-01-02")
				careerMu.Lock()
				alreadySent := (careerStore.LastBriefing == todayKey)
				if !alreadySent {
					careerStore.LastBriefing = todayKey
					_ = saveCareerStoreLocked()
				}
				careerMu.Unlock()

				if !alreadySent {
					cfg, err := loadConfig()
					if err == nil && cfg != nil {
						gCfg := getGuildSettings(SunfireServerID)
						targetChannel := gCfg.JobsChannelID
						if targetChannel == "" {
							targetChannel = cfg.LoggingChannel
						}

						if targetChannel != "" {
							embed := buildCareerStandupEmbed()
							msgSend := &discordgo.MessageSend{
								Content: "🌅 Good morning Daniel! Here is your daily career standup briefing:",
								Embeds:  []*discordgo.MessageEmbed{embed},
							}
							if _, err := sendNotifierComplex(s, targetChannel, msgSend); err != nil {
								log.Printf("career daemon: failed posting daily standup: %v", err)
							} else {
								log.Printf("career daemon: posted daily standup to %s", targetChannel)
							}
						}
					}
				}
			}
		}
	}
}

func followUpEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: msg,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
}
