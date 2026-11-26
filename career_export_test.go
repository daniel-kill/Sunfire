package main

import (
	"strings"
	"testing"
	"time"
)

func TestExportCareerStatusMarkdown(t *testing.T) {
	now := time.Now()
	apps := []JobApplication{
		{
			ID:           "app-1",
			Organization: "Acme Cloud",
			Position:     "Senior DevOps Engineer",
			Status:       "Interviewing",
			DateApplied:  now.Add(-72 * time.Hour),
			FollowUpDate: now.Add(24 * time.Hour),
			FollowedUp:   false,
			URL:          "https://example.com/jobs/devops",
			Notes:        "Round 2 technical completed",
		},
		{
			ID:           "app-2",
			Organization: "Beta Tech",
			Position:     "Systems Architect",
			Status:       "Applied",
			DateApplied:  now.Add(-10 * 24 * time.Hour),
			FollowUpDate: now.Add(-3 * 24 * time.Hour),
			FollowedUp:   false,
			Notes:        "Applied via referral",
		},
	}

	todos := []CareerActionItem{
		{ID: "todo-1", Title: "Submit official transcripts", Category: "Education", Status: "Pending"},
	}

	certs := []CertGoal{
		{Program: "AZ-900", Provider: "Microsoft", Priority: "High", Status: "In Progress", Relevance: "Cloud Infrastructure"},
	}

	md := ExportCareerStatusMarkdown(apps, "Active", todos, certs)

	if !strings.Contains(md, "# 📊 Career Applications Pipeline") {
		t.Errorf("expected markdown header")
	}
	if !strings.Contains(md, "Senior DevOps Engineer") {
		t.Errorf("expected Position in markdown")
	}
	if !strings.Contains(md, "Acme Cloud") {
		t.Errorf("expected Organization in markdown")
	}
	if !strings.Contains(md, "Round 2 technical completed") {
		t.Errorf("expected Notes in markdown")
	}
	if !strings.Contains(md, "Submit official transcripts") {
		t.Errorf("expected ActionItem in markdown")
	}
	if !strings.Contains(md, "AZ-900") {
		t.Errorf("expected CertGoal in markdown")
	}
	if !strings.Contains(md, "overdue") {
		t.Errorf("expected overdue notice for app-2 in markdown")
	}
}

func TestExportCareerStatusPDF(t *testing.T) {
	now := time.Now()
	apps := []JobApplication{
		{
			ID:           "app-101",
			Organization: "CyberDyne Systems",
			Position:     "Infrastructure Lead",
			Status:       "Interviewing",
			DateApplied:  now.Add(-48 * time.Hour),
			FollowUpDate: now.Add(48 * time.Hour),
			FollowedUp:   true,
			URL:          "https://cyberdyne.example.com",
			Notes:        "Screening passed",
		},
		{
			ID:           "app-102",
			Organization: "Initech Corp",
			Position:     "Site Reliability Engineer",
			Status:       "Applied",
			DateApplied:  now.Add(-120 * time.Hour),
			FollowUpDate: now.Add(-24 * time.Hour),
			FollowedUp:   false,
			Notes:        "Followup overdue",
		},
	}

	pdfBytes, err := ExportCareerStatusPDF(apps, "Active", 1, 1, 0, 1)
	if err != nil {
		t.Fatalf("ExportCareerStatusPDF failed: %v", err)
	}

	if len(pdfBytes) < 500 {
		t.Fatalf("expected non-trivial PDF byte size, got %d bytes", len(pdfBytes))
	}

	// Verify PDF magic header
	if !strings.HasPrefix(string(pdfBytes[:8]), "%PDF-1.4") {
		t.Errorf("expected %%PDF-1.4 header, got %s", string(pdfBytes[:8]))
	}

	// Verify text extraction using github.com/dslipak/pdf
	extractedText, err := extractTextFromPDFBytes(pdfBytes)
	if err != nil {
		t.Fatalf("extractTextFromPDFBytes failed to parse generated PDF: %v", err)
	}

	if !strings.Contains(extractedText, "SUNFIRE CAREER APPLICATIONS PIPELINE") {
		t.Errorf("expected banner text in extracted PDF text: %s", extractedText)
	}
	if !strings.Contains(extractedText, "CyberDyne Systems") {
		t.Errorf("expected CyberDyne Systems in extracted PDF text")
	}
	if !strings.Contains(extractedText, "Initech Corp") {
		t.Errorf("expected Initech Corp in extracted PDF text")
	}
	if !strings.Contains(extractedText, "Infrastructure Lead") {
		t.Errorf("expected Infrastructure Lead in extracted PDF text")
	}
	if !strings.Contains(extractedText, "DUE NOW") {
		t.Errorf("expected DUE NOW alert for overdue application in extracted PDF text")
	}
}

func TestExportCareerStatusPDF_MultiPage(t *testing.T) {
	now := time.Now()
	// Generate 35 applications to force a 2nd page
	var apps []JobApplication
	for i := 1; i <= 35; i++ {
		apps = append(apps, JobApplication{
			ID:           strings.ToLower(strings.ReplaceAll(string(rune('a'+i%26)), " ", "")),
			Organization: strings.Repeat("Company-", 2),
			Position:     "Software / Systems Engineer",
			Status:       "Applied",
			DateApplied:  now.Add(time.Duration(-i*24) * time.Hour),
			FollowUpDate: now.Add(time.Duration(i*24) * time.Hour),
			Notes:        "Application record",
		})
	}

	pdfBytes, err := ExportCareerStatusPDF(apps, "All", 35, 0, 0, 0)
	if err != nil {
		t.Fatalf("ExportCareerStatusPDF multi-page failed: %v", err)
	}

	extractedText, err := extractTextFromPDFBytes(pdfBytes)
	if err != nil {
		t.Fatalf("extractTextFromPDFBytes failed: %v", err)
	}

	if !strings.Contains(extractedText, "Page 2 of 2") {
		t.Errorf("expected multi-page indicator 'Page 2 of 2' in text, got: %s", extractedText)
	}
}
