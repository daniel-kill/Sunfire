package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseCost(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
		wantErr  bool
	}{
		{"19.99", 19.99, false},
		{"$19.99", 19.99, false},
		{"$199.99 USD", 199.99, false},
		{"10", 10.0, false},
		{"$0", 0.0, false},
		{"-5.00", 0.0, true},
		{"abc", 0.0, true},
	}

	for _, tc := range tests {
		val, err := parseCost(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseCost(%q) error = %v, wantErr = %v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && val != tc.expected {
			t.Errorf("parseCost(%q) = %v, expected %v", tc.input, val, tc.expected)
		}
	}
}

func TestParseFlexibleDate(t *testing.T) {
	now := time.Now()

	// Test ISO format
	d1, err := parseFlexibleDate("2027-05-06")
	if err != nil {
		t.Fatalf("parseFlexibleDate(2027-05-06) error: %v", err)
	}
	if d1.Year() != 2027 || d1.Month() != time.May || d1.Day() != 6 {
		t.Errorf("unexpected date: %v", d1)
	}

	// Test standard US format
	d2, err := parseFlexibleDate("May 6, 2027")
	if err != nil {
		t.Fatalf("parseFlexibleDate(May 6, 2027) error: %v", err)
	}
	if d2.Year() != 2027 || d2.Month() != time.May || d2.Day() != 6 {
		t.Errorf("unexpected date: %v", d2)
	}

	// Test relative offset
	d3, err := parseFlexibleDate("in 30 days")
	if err != nil {
		t.Fatalf("parseFlexibleDate(in 30 days) error: %v", err)
	}
	expectedApprox := now.AddDate(0, 0, 30)
	if d3.Year() != expectedApprox.Year() || d3.Month() != expectedApprox.Month() || d3.Day() != expectedApprox.Day() {
		t.Errorf("unexpected relative date: %v, expected approx: %v", d3, expectedApprox)
	}
}

func TestAdvanceRenewalDate(t *testing.T) {
	base := time.Date(2027, time.May, 6, 0, 0, 0, 0, time.UTC)

	// Monthly
	m := advanceRenewalDate(base, CycleMonthly)
	if m.Year() != 2027 || m.Month() != time.June || m.Day() != 6 {
		t.Errorf("advanceRenewalDate Monthly = %v, expected 2027-06-06", m)
	}

	// Yearly
	y := advanceRenewalDate(base, CycleYearly)
	if y.Year() != 2028 || y.Month() != time.May || y.Day() != 6 {
		t.Errorf("advanceRenewalDate Yearly = %v, expected 2028-05-06", y)
	}

	// Quarterly
	q := advanceRenewalDate(base, CycleQuarterly)
	if q.Year() != 2027 || q.Month() != time.August || q.Day() != 6 {
		t.Errorf("advanceRenewalDate Quarterly = %v, expected 2027-08-06", q)
	}

	// Weekly
	w := advanceRenewalDate(base, CycleWeekly)
	expectedW := base.AddDate(0, 0, 7)
	if !w.Equal(expectedW) {
		t.Errorf("advanceRenewalDate Weekly = %v, expected %v", w, expectedW)
	}
}

func TestCalculateBurnRates(t *testing.T) {
	subs := []Subscription{
		{
			ID:           "sub-1",
			Cost:         19.99,
			BillingCycle: CycleMonthly,
			Status:       StatusActive,
		},
		{
			ID:           "sub-2",
			Cost:         199.99,
			BillingCycle: CycleYearly,
			Status:       StatusActive,
		},
		{
			ID:           "sub-3",
			Cost:         10.00,
			BillingCycle: CycleMonthly,
			Status:       StatusActive,
		},
		{
			ID:           "sub-4",
			Cost:         50.00,
			BillingCycle: CycleMonthly,
			Status:       StatusCancelled, // Should not count towards burn rate
		},
	}

	monthly, yearly := calculateBurnRates(subs)

	expectedMonthly := 19.99 + (199.99 / 12.0) + 10.00
	expectedYearly := expectedMonthly * 12.0

	if monthly < expectedMonthly-0.01 || monthly > expectedMonthly+0.01 {
		t.Errorf("calculateBurnRates monthly = %v, expected %v", monthly, expectedMonthly)
	}
	if yearly < expectedYearly-0.05 || yearly > expectedYearly+0.05 {
		t.Errorf("calculateBurnRates yearly = %v, expected %v", yearly, expectedYearly)
	}
}

func TestFindSubscriptionIndex(t *testing.T) {
	subs := []Subscription{
		{ID: "google-ai-pro", Name: "Google AI Pro (5 TB)"},
		{ID: "microsoft-365", Name: "Microsoft 365 Premium"},
		{ID: "github-copilot", Name: "GitHub Copilot Pro"},
	}

	// Exact ID
	if idx := findSubscriptionIndex("google-ai-pro", subs); idx != 0 {
		t.Errorf("findSubscriptionIndex exact ID = %d, expected 0", idx)
	}

	// Case-insensitive ID
	if idx := findSubscriptionIndex("MICROSOFT-365", subs); idx != 1 {
		t.Errorf("findSubscriptionIndex case-insensitive ID = %d, expected 1", idx)
	}

	// Substring of Name
	if idx := findSubscriptionIndex("copilot", subs); idx != 2 {
		t.Errorf("findSubscriptionIndex substring = %d, expected 2", idx)
	}

	// Not found
	if idx := findSubscriptionIndex("nonexistent", subs); idx != -1 {
		t.Errorf("findSubscriptionIndex nonexistent = %d, expected -1", idx)
	}
}

func TestGenerateSubscriptionID(t *testing.T) {
	existing := []Subscription{
		{ID: "google-ai-pro"},
	}

	// First time
	id1 := generateSubscriptionID("Google AI Pro", existing)
	if id1 != "google-ai-pro-2" {
		t.Errorf("generateSubscriptionID collision expected google-ai-pro-2, got %s", id1)
	}

	// New slug
	id2 := generateSubscriptionID("Claude Pro", existing)
	if id2 != "claude-pro" {
		t.Errorf("generateSubscriptionID expected claude-pro, got %s", id2)
	}
}

func TestImportSubscriptionsFromFile(t *testing.T) {
	// Create a temp file replicating Subscriptions.txt
	tmpDir := t.TempDir()
	samplePath := filepath.Join(tmpDir, "Subscriptions.txt")
	sampleContent := `Google AI Pro (5 TB)Google AI Pro (5 TB) $19.99 per month for 12 months
Microsoft 365 Premium Next charge on May 6, 2027, $199.99 plus applicable
GitHub Copilot Pro $10 USD per month
`
	if err := os.WriteFile(samplePath, []byte(sampleContent), 0600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	items, err := importSubscriptionsFromFile(samplePath)
	if err != nil {
		t.Fatalf("importSubscriptionsFromFile failed: %v", err)
	}

	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	// 1. Google AI Pro
	if items[0].Name != "Google AI Pro (5 TB)" || items[0].Cost != 19.99 || items[0].BillingCycle != CycleMonthly {
		t.Errorf("item 0 mismatch: %+v", items[0])
	}

	// 2. Microsoft 365 Premium
	if items[1].Name != "Microsoft 365 Premium" || items[1].Cost != 199.99 || items[1].BillingCycle != CycleYearly {
		t.Errorf("item 1 mismatch: %+v", items[1])
	}
	if items[1].NextRenewal.Year() != 2027 || items[1].NextRenewal.Month() != time.May || items[1].NextRenewal.Day() != 6 {
		t.Errorf("item 1 date mismatch: %v", items[1].NextRenewal)
	}

	// 3. GitHub Copilot Pro
	if items[2].Name != "GitHub Copilot Pro" || items[2].Cost != 10.00 || items[2].BillingCycle != CycleMonthly {
		t.Errorf("item 2 mismatch: %+v", items[2])
	}
}

func TestSubscriptionsCommandSpec(t *testing.T) {
	spec := subscriptionsCommandSpec()
	if spec.Command.Name != "subscriptions" {
		t.Errorf("expected command name 'subscriptions', got %s", spec.Command.Name)
	}
	if len(spec.Command.Options) < 4 {
		t.Errorf("expected at least 4 subcommands, got %d", len(spec.Command.Options))
	}

	subcommandNames := make(map[string]bool)
	for _, opt := range spec.Command.Options {
		subcommandNames[opt.Name] = true
	}

	for _, required := range []string{"list", "add", "modify", "delete"} {
		if !subcommandNames[required] {
			t.Errorf("missing required subcommand: %s", required)
		}
	}
}
