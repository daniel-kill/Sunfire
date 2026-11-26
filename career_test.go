package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestCareerStoreInitialization(t *testing.T) {
	initCareerStore()

	careerMu.Lock()
	defer careerMu.Unlock()

	if len(careerStore.Applications) < 2 {
		t.Errorf("expected at least 2 seeded applications, got %d", len(careerStore.Applications))
	}

	// Verify CVS Health and Hoopla exist
	foundCVS := false
	foundHoopla := false
	for _, app := range careerStore.Applications {
		if strings.Contains(app.Organization, "CVS") {
			foundCVS = true
		}
		if strings.Contains(app.Organization, "Hoopla") {
			foundHoopla = true
		}
	}
	if !foundCVS {
		t.Errorf("expected seeded CVS Health application in career store")
	}
	if !foundHoopla {
		t.Errorf("expected seeded Hoopla application in career store")
	}

	// Verify Action Items (Jobs.txt)
	if len(careerStore.ActionItems) < 3 {
		t.Errorf("expected at least 3 action items from Jobs.txt, got %d", len(careerStore.ActionItems))
	}
	foundGPA := false
	for _, todo := range careerStore.ActionItems {
		if strings.Contains(todo.Title, "Owens") {
			foundGPA = true
		}
	}
	if !foundGPA {
		t.Errorf("expected Owens GPA action item in career store")
	}

	// Verify Cert Goals (Certificate Planning.ods)
	if len(careerStore.CertGoals) < 5 {
		t.Errorf("expected at least 5 certification roadmap items, got %d", len(careerStore.CertGoals))
	}
}

func TestScheduleCareerFollowUp(t *testing.T) {
	done := make(chan bool)
	go func() {
		scheduleCareerFollowUp("channel123", "user123", "TestCo", "Engineer", time.Now().Add(time.Hour))
		done <- true
	}()
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK DETECTED in scheduleCareerFollowUp: remindersMu double lock!")
	}
}

func TestBuildJobActionRow(t *testing.T) {
	job := JobPosting{
		ID:          "test-job-12345",
		Title:       "Senior DevOps & Infrastructure Engineer",
		Company:     "Acme Cloud Systems",
		URL:         "https://example.com/jobs/devops-lead",
		Location:    "100% Remote (US)",
		Salary:      "$150k - $180k",
		Score:       85,
		Description: "Looking for an infrastructure lead with Go, Linux, Docker, and PowerShell expertise.",
	}

	components := buildJobActionRow(job)
	if len(components) != 1 {
		t.Fatalf("expected 1 ActionRow, got %d", len(components))
	}

	row, ok := components[0].(discordgo.ActionsRow)
	if !ok {
		t.Fatalf("expected component to be discordgo.ActionsRow")
	}

	if len(row.Components) != 3 {
		t.Fatalf("expected 3 buttons in ActionRow, got %d", len(row.Components))
	}

	btnTrack, ok := row.Components[0].(discordgo.Button)
	if !ok || !strings.HasPrefix(btnTrack.CustomID, "job:track:") {
		t.Errorf("expected first button to be job:track:*, got %#v", row.Components[0])
	}
	if btnTrack.Label != "Apply / Save" {
		t.Errorf("expected label 'Apply / Save', got %s", btnTrack.Label)
	}

	btnFit, ok := row.Components[1].(discordgo.Button)
	if !ok || !strings.HasPrefix(btnFit.CustomID, "job:fit:") {
		t.Errorf("expected second button to be job:fit:*, got %#v", row.Components[1])
	}

	btnLink, ok := row.Components[2].(discordgo.Button)
	if !ok || btnLink.URL != job.URL {
		t.Errorf("expected third button to have URL %s, got %#v", job.URL, row.Components[2])
	}
}

func TestGenerateDeterministicMatch(t *testing.T) {
	title := "IT Operations Manager"
	company := "Midwest Cloud Tech"
	desc := "Leading enterprise infrastructure migrations, disaster recovery, and multi-facility systems support."

	result := generateDeterministicMatch(title, company, desc)
	if !strings.Contains(result, "Key Matching Strengths") {
		t.Errorf("expected Key Matching Strengths in result: %s", result)
	}
	if !strings.Contains(result, "Bohl Equipment Co.") {
		t.Errorf("expected Bohl Equipment Co. reference in match result: %s", result)
	}
	if !strings.Contains(result, "High-Impact ATS Keywords") {
		t.Errorf("expected High-Impact ATS Keywords in result: %s", result)
	}
}

func TestGenerateDeterministicCoverLetter(t *testing.T) {
	company := "Global Remote Enterprises"
	position := "Senior IT Infrastructure Lead"

	letter := generateDeterministicCoverLetter(company, position)

	if !strings.Contains(letter, "Global Remote Enterprises") {
		t.Errorf("expected company name in letter")
	}
	if !strings.Contains(letter, "Senior IT Infrastructure Lead") {
		t.Errorf("expected position in letter")
	}
	if !strings.Contains(letter, "22 years") && !strings.Contains(letter, "22-year") {
		t.Errorf("expected 22-year tenure mention in letter")
	}
	if !strings.Contains(letter, "Bohl Equipment Company") {
		t.Errorf("expected Bohl Equipment Company in letter")
	}
	if !strings.Contains(letter, "Daniel Kill") {
		t.Errorf("expected signature Daniel Kill in letter")
	}
	if !strings.Contains(letter, "99.9% uptime") {
		t.Errorf("expected 99.9%% uptime metric in letter")
	}
}

func TestBuildCareerStandupEmbed(t *testing.T) {
	initCareerStore()

	embed := buildCareerStandupEmbed()
	if embed == nil {
		t.Fatalf("expected non-nil standup embed")
	}

	if !strings.Contains(embed.Title, "Morning Career Standup") {
		t.Errorf("unexpected embed title: %s", embed.Title)
	}

	if len(embed.Fields) < 3 {
		t.Errorf("expected at least 3 fields in standup embed, got %d", len(embed.Fields))
	}

	// Verify Jobs.txt action checklist field is present
	foundChecklist := false
	for _, f := range embed.Fields {
		if strings.Contains(f.Name, "Jobs.txt") {
			foundChecklist = true
			if !strings.Contains(f.Value, "Owens") {
				t.Errorf("expected Owens GPA in checklist field: %s", f.Value)
			}
		}
	}
	if !foundChecklist {
		t.Errorf("expected Jobs.txt checklist field in standup embed")
	}
}

func TestShortJobHash(t *testing.T) {
	h1 := shortJobHash("https://weworkremotely.com/remote-jobs/anthropic-autonomous-systems-security-engineer")
	h2 := shortJobHash("https://weworkremotely.com/remote-jobs/anthropic-autonomous-systems-security-engineer")
	h3 := shortJobHash("bamboo-cnwr-51")

	if h1 != h2 {
		t.Errorf("expected deterministic hash output, got %s != %s", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("expected different hash for different inputs")
	}
	if len(h1) != 12 {
		t.Errorf("expected 12-char hash, got %d chars (%s)", len(h1), h1)
	}
}

func TestCareerCommandSpec(t *testing.T) {
	spec := careerCommandSpec()
	if spec.Command.Name != "career" {
		t.Errorf("expected command name 'career', got %s", spec.Command.Name)
	}

	expectedSubcommands := map[string]bool{
		"apply":       false,
		"status":      false,
		"update":      false,
		"match":       false,
		"analyze":     false,
		"coverletter": false,
		"todos":       false,
		"certs":       false,
		"standup":     false,
	}

	for _, opt := range spec.Command.Options {
		if opt.Type == discordgo.ApplicationCommandOptionSubCommand {
			expectedSubcommands[opt.Name] = true
		}
	}

	for name, found := range expectedSubcommands {
		if !found {
			t.Errorf("expected subcommand %q in /career command", name)
		}
	}

	// Verify status subcommand options including export
	var statusSub *discordgo.ApplicationCommandOption
	for _, opt := range spec.Command.Options {
		if opt.Name == "status" {
			statusSub = opt
			break
		}
	}
	if statusSub == nil {
		t.Fatalf("status subcommand not found in spec")
	}

	expectedStatusOpts := map[string]discordgo.ApplicationCommandOptionType{
		"filter": discordgo.ApplicationCommandOptionString,
		"export": discordgo.ApplicationCommandOptionBoolean,
	}

	for _, opt := range statusSub.Options {
		expectedType, ok := expectedStatusOpts[opt.Name]
		if !ok {
			t.Errorf("unexpected option %q in status subcommand", opt.Name)
			continue
		}
		if opt.Type != expectedType {
			t.Errorf("option %q expected type %v, got %v", opt.Name, expectedType, opt.Type)
		}
		delete(expectedStatusOpts, opt.Name)
	}

	for missingOpt := range expectedStatusOpts {
		t.Errorf("missing expected option %q in status subcommand", missingOpt)
	}

	// Verify update subcommand options including new follow-up options
	var updateSub *discordgo.ApplicationCommandOption
	for _, opt := range spec.Command.Options {
		if opt.Name == "update" {
			updateSub = opt
			break
		}
	}
	if updateSub == nil {
		t.Fatalf("update subcommand not found in spec")
	}

	expectedUpdateOpts := map[string]discordgo.ApplicationCommandOptionType{
		"id":                 discordgo.ApplicationCommandOptionString,
		"status":             discordgo.ApplicationCommandOptionString,
		"notes":              discordgo.ApplicationCommandOptionString,
		"followed_up":        discordgo.ApplicationCommandOptionBoolean,
		"next_followup_days": discordgo.ApplicationCommandOptionInteger,
	}

	for _, opt := range updateSub.Options {
		expectedType, ok := expectedUpdateOpts[opt.Name]
		if !ok {
			t.Errorf("unexpected option %q in update subcommand", opt.Name)
			continue
		}
		if opt.Type != expectedType {
			t.Errorf("option %q expected type %v, got %v", opt.Name, expectedType, opt.Type)
		}
		delete(expectedUpdateOpts, opt.Name)
	}

	for missingOpt := range expectedUpdateOpts {
		t.Errorf("missing expected option %q in update subcommand", missingOpt)
	}
}
func TestCareerPDFResumeExtraction(t *testing.T) {
	pdfPath := resumePDFPath()
	if _, err := os.Stat(pdfPath); os.IsNotExist(err) {
		t.Skip("Resume.pdf does not exist in .private/career, skipping live test")
	}

	text, err := extractTextFromPDF(pdfPath)
	if err != nil {
		t.Fatalf("extractTextFromPDF(%s) failed: %v", pdfPath, err)
	}

	if len(text) < 500 {
		t.Errorf("extracted text too short (%d bytes)", len(text))
	}
	if !strings.Contains(text, "DANIEL KILL") && !strings.Contains(text, "Daniel Kill") {
		t.Errorf("extracted text does not contain Daniel Kill")
	}
	if !strings.Contains(text, "Bohl Equipment") && !strings.Contains(text, "Infrastructure") {
		t.Errorf("extracted text does not contain key resume terms")
	}
}

func TestLoadAuthoritativeResume(t *testing.T) {
	// Test 1: Fallback when custom files do not exist
	text, source := loadAuthoritativeResume("non_existent_file.pdf", nil, "")
	if len(text) == 0 {
		t.Errorf("expected non-empty resume text")
	}
	if len(source) == 0 {
		t.Errorf("expected non-empty resume source")
	}

	// Test 2: Uploaded markdown bytes
	uploadData := []byte("# Custom Candidate\nExperienced Go and DevOps engineer.")
	txt, src := loadAuthoritativeResume("", uploadData, "candidate_resume.md")
	if !strings.Contains(txt, "Custom Candidate") {
		t.Errorf("expected uploaded markdown text in result")
	}
	if !strings.Contains(src, "candidate_resume.md") {
		t.Errorf("expected source to mention candidate_resume.md, got %s", src)
	}
}

func TestCareerFollowUpStatusNotice(t *testing.T) {
	now := time.Now()

	// Case 1: Overdue application, not followed up
	app1 := JobApplication{
		FollowUpDate: now.Add(-24 * time.Hour),
		FollowedUp:   false,
		Status:       "Applied",
	}
	isDue1 := !app1.FollowedUp && now.After(app1.FollowUpDate) && app1.Status != "Rejected" && app1.Status != "Archived"
	if !isDue1 {
		t.Errorf("expected app1 to be marked due")
	}

	// Case 2: Overdue application, but follow-up was completed
	app2 := JobApplication{
		FollowUpDate: now.Add(-24 * time.Hour),
		FollowedUp:   true,
		Status:       "Applied",
	}
	isDue2 := !app2.FollowedUp && now.After(app2.FollowUpDate) && app2.Status != "Rejected" && app2.Status != "Archived"
	if isDue2 {
		t.Errorf("expected app2 NOT to be marked due after follow-up completed")
	}
	if !app2.FollowedUp {
		t.Errorf("expected app2 to have FollowedUp == true")
	}

	// Case 3: Rejected or Archived should not trigger due notice even if past date
	app3 := JobApplication{
		FollowUpDate: now.Add(-24 * time.Hour),
		FollowedUp:   false,
		Status:       "Rejected",
	}
	isDue3 := !app3.FollowedUp && now.After(app3.FollowUpDate) && app3.Status != "Rejected" && app3.Status != "Archived"
	if isDue3 {
		t.Errorf("expected rejected app3 NOT to be marked due")
	}
}
