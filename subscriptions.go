// subscriptions.go — Personal Subscription & Recurring Bill Tracker for Sunfire.
// Tracks recurring SaaS, cloud, media, and productivity subscriptions, provides
// financial burn rate summaries, and sends automated Discord DM alerts within 7 days
// of expiration or renewal.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// BillingCycle represents the recurrence period for a subscription.
type BillingCycle string

const (
	CycleMonthly   BillingCycle = "Monthly"
	CycleYearly    BillingCycle = "Yearly"
	CycleWeekly    BillingCycle = "Weekly"
	CycleQuarterly BillingCycle = "Quarterly"
	CycleLifetime  BillingCycle = "Lifetime"
)

// SubscriptionStatus represents the current status of a subscription.
type SubscriptionStatus string

const (
	StatusActive    SubscriptionStatus = "Active"
	StatusPaused    SubscriptionStatus = "Paused"
	StatusCancelled SubscriptionStatus = "Cancelled"
	StatusExpired   SubscriptionStatus = "Expired"
)

// Subscription represents an individual tracked recurring service.
type Subscription struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Cost         float64            `json:"cost"`
	Currency     string             `json:"currency"`
	BillingCycle BillingCycle       `json:"billingCycle"`
	NextRenewal  time.Time          `json:"nextRenewal"`
	Category     string             `json:"category,omitempty"` // "AI", "Developer Tools", "Productivity", "Cloud", "Entertainment", "Finance", "Other"
	Status       SubscriptionStatus `json:"status"`
	AutoRenew    bool               `json:"autoRenew"`
	Notes        string             `json:"notes,omitempty"`
	UserID       string             `json:"userId,omitempty"`       // Discord User ID to alert via DM
	LastNotified time.Time          `json:"lastNotified,omitempty"` // Timestamp of the last expiration alert sent
	CreatedAt    time.Time          `json:"createdAt"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}

// SubscriptionStore holds all persisted subscriptions.
type SubscriptionStore struct {
	Subscriptions []Subscription `json:"subscriptions"`
	LastCheck     time.Time      `json:"lastCheck,omitempty"`
}

var (
	subMu          sync.Mutex
	subStore       SubscriptionStore
	subStoreLoaded bool
)

// subscriptionsDataPath returns the path to subscriptions.json in Data/Subscriptions.
func subscriptionsDataPath() string {
	return dataPath("Subscriptions", "subscriptions.json")
}

// initSubscriptionStore loads existing subscriptions or auto-seeds from Subscriptions.txt.
func initSubscriptionStore() {
	subMu.Lock()
	defer subMu.Unlock()
	if subStoreLoaded {
		return
	}

	path := subscriptionsDataPath()
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &subStore); err == nil && len(subStore.Subscriptions) > 0 {
			subStoreLoaded = true
			return
		}
	}

	// Auto-seed from /home/daniel/Repos/_temp/Subscriptions.txt if available
	seedPath := "/home/daniel/Repos/_temp/Subscriptions.txt"
	if seeded, err := importSubscriptionsFromFile(seedPath); err == nil && len(seeded) > 0 {
		subStore.Subscriptions = seeded
		_ = saveSubscriptionStoreLocked()
		log.Printf("Subscriptions: seeded %d entries from %s", len(seeded), seedPath)
	} else {
		// Default built-in seed based on reviewed Subscriptions.txt
		now := time.Now()
		subStore.Subscriptions = []Subscription{
			{
				ID:           "google-ai-pro",
				Name:         "Google AI Pro (5 TB)",
				Cost:         19.99,
				Currency:     "USD",
				BillingCycle: CycleMonthly,
				NextRenewal:  now.AddDate(0, 1, 0),
				Category:     "AI",
				Status:       StatusActive,
				AutoRenew:    true,
				Notes:        "5 TB storage, $19.99/mo for 12 months promo",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
			{
				ID:           "microsoft-365-premium",
				Name:         "Microsoft 365 Premium",
				Cost:         199.99,
				Currency:     "USD",
				BillingCycle: CycleYearly,
				NextRenewal:  time.Date(2027, time.May, 6, 0, 0, 0, 0, time.Local),
				Category:     "Productivity",
				Status:       StatusActive,
				AutoRenew:    true,
				Notes:        "Next charge on May 6, 2027 ($199.99 plus applicable taxes)",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
			{
				ID:           "github-copilot-pro",
				Name:         "GitHub Copilot Pro",
				Cost:         10.00,
				Currency:     "USD",
				BillingCycle: CycleMonthly,
				NextRenewal:  now.AddDate(0, 1, 0),
				Category:     "Developer Tools",
				Status:       StatusActive,
				AutoRenew:    true,
				Notes:        "$10 USD per month",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		}
		_ = saveSubscriptionStoreLocked()
	}

	subStoreLoaded = true
}

func saveSubscriptionStoreLocked() error {
	path := subscriptionsDataPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory for subscriptions: %w", err)
	}
	data, err := json.MarshalIndent(subStore, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling subscriptions: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing temp subscription store: %w", err)
	}
	return os.Rename(tmp, path)
}

func saveSubscriptionStore() error {
	subMu.Lock()
	defer subMu.Unlock()
	return saveSubscriptionStoreLocked()
}

// subscriptionsCommandSpec registers the /subscriptions slash command in Sunfire.
func subscriptionsCommandSpec() commandSpec {
	return commandSpec{
		Command: &discordgo.ApplicationCommand{
			Name:        "subscriptions",
			Description: "Track, manage, and monitor recurring subscriptions & renewal alerts",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "list",
					Description: "List active subscriptions, upcoming renewal countdowns, and burn rate",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "filter",
							Description: "Filter subscriptions to display",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Active Subscriptions (Default)", Value: "active"},
								{Name: "Expiring Soon (≤ 7 days)", Value: "expiring"},
								{Name: "All Subscriptions", Value: "all"},
								{Name: "Monthly Cycle Only", Value: "monthly"},
								{Name: "Yearly Cycle Only", Value: "yearly"},
								{Name: "Cancelled Only", Value: "cancelled"},
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "sort",
							Description: "Order subscriptions",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Renewal Date (Soonest first)", Value: "renewal"},
								{Name: "Cost (Highest first)", Value: "cost_desc"},
								{Name: "Cost (Lowest first)", Value: "cost_asc"},
								{Name: "Name (Alphabetical)", Value: "name"},
							},
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "add",
					Description: "Track a new recurring subscription",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Service name (e.g. Claude Pro, Netflix, Spotify)", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "cost", Description: "Price per cycle (e.g. 19.99 or $10.00)", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "renewal", Description: "Next renewal date (YYYY-MM-DD e.g. 2027-05-06, or 'May 6 2027')", Required: true},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "cycle",
							Description: "Billing frequency (default: Monthly)",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Monthly", Value: "Monthly"},
								{Name: "Yearly", Value: "Yearly"},
								{Name: "Weekly", Value: "Weekly"},
								{Name: "Quarterly", Value: "Quarterly"},
								{Name: "Lifetime / One-off", Value: "Lifetime"},
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "category",
							Description: "Service category",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "AI & Machine Learning", Value: "AI"},
								{Name: "Developer Tools & Hosting", Value: "Developer Tools"},
								{Name: "Productivity & Office", Value: "Productivity"},
								{Name: "Cloud & Storage", Value: "Cloud"},
								{Name: "Entertainment & Streaming", Value: "Entertainment"},
								{Name: "Finance & Security", Value: "Finance"},
								{Name: "Other", Value: "Other"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionString, Name: "notes", Description: "Plan tier (e.g. 5 TB), payment card, or terms", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "modify",
					Description: "Update properties or status of an existing subscription",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Subscription ID or name substring", Required: true},
						{Type: discordgo.ApplicationCommandOptionString, Name: "name", Description: "Updated service name", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "cost", Description: "Updated price (e.g. 19.99)", Required: false},
						{Type: discordgo.ApplicationCommandOptionString, Name: "renewal", Description: "Updated renewal date (YYYY-MM-DD or date string)", Required: false},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "cycle",
							Description: "Updated billing cycle",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Monthly", Value: "Monthly"},
								{Name: "Yearly", Value: "Yearly"},
								{Name: "Weekly", Value: "Weekly"},
								{Name: "Quarterly", Value: "Quarterly"},
								{Name: "Lifetime", Value: "Lifetime"},
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "category",
							Description: "Updated category",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "AI", Value: "AI"},
								{Name: "Developer Tools", Value: "Developer Tools"},
								{Name: "Productivity", Value: "Productivity"},
								{Name: "Cloud", Value: "Cloud"},
								{Name: "Entertainment", Value: "Entertainment"},
								{Name: "Finance", Value: "Finance"},
								{Name: "Other", Value: "Other"},
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "status",
							Description: "Updated status",
							Required:    false,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{Name: "Active", Value: "Active"},
								{Name: "Paused", Value: "Paused"},
								{Name: "Cancelled", Value: "Cancelled"},
							},
						},
						{Type: discordgo.ApplicationCommandOptionString, Name: "notes", Description: "Updated notes", Required: false},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "delete",
					Description: "Remove a tracked subscription from the database",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Subscription ID or name substring", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "renew",
					Description: "Mark a subscription as paid and advance renewal by one billing cycle",
					Options: []*discordgo.ApplicationCommandOption{
						{Type: discordgo.ApplicationCommandOptionString, Name: "id", Description: "Subscription ID or name substring", Required: true},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "stats",
					Description: "View financial burn rate, annual projections, and category distribution",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "upcoming",
					Description: "Quick view of bills due within the next 30 days",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "test-alert",
					Description: "Trigger an immediate DM test alert for subscriptions renewing soon",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "import",
					Description: "Import/sync subscriptions from /home/daniel/Repos/_temp/Subscriptions.txt",
				},
			},
		},
		Handler: handleSubscriptionsCommand,
	}
}

// handleSubscriptionsCommand routes subcommands for /subscriptions.
func handleSubscriptionsCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initSubscriptionStore()
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		showSubscriptionsHelp(s, i)
		return
	}

	sub := data.Options[0]
	switch sub.Name {
	case "list":
		handleSubList(s, i, sub)
	case "add":
		handleSubAdd(s, i, sub)
	case "modify":
		handleSubModify(s, i, sub)
	case "delete":
		handleSubDelete(s, i, sub)
	case "renew":
		handleSubRenew(s, i, sub)
	case "stats":
		handleSubStats(s, i)
	case "upcoming":
		handleSubUpcoming(s, i)
	case "test-alert":
		handleSubTestAlert(s, i)
	case "import":
		handleSubImport(s, i)
	default:
		showSubscriptionsHelp(s, i)
	}
}

func showSubscriptionsHelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
	embed := &discordgo.MessageEmbed{
		Title:       "💳 Sunfire Subscription & Bill Tracker",
		Description: "Track recurring SaaS and cloud services with automated 7-day expiration DM alerts.",
		Color:       0x3b82f6,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "`/subscriptions list`", Value: "View tracked subscriptions, status, and monthly/annual burn rate.", Inline: false},
			{Name: "`/subscriptions add`", Value: "Add a service with name, cost, next renewal date, and cycle.", Inline: false},
			{Name: "`/subscriptions modify`", Value: "Update cost, renewal date, status (Active/Cancelled), or notes.", Inline: false},
			{Name: "`/subscriptions delete`", Value: "Remove a subscription permanently from the tracker.", Inline: false},
			{Name: "`/subscriptions renew`", Value: "Advance next renewal date by 1 cycle after payment.", Inline: false},
			{Name: "`/subscriptions stats`", Value: "Financial summary: monthly burn rate, yearly projected spend, category totals.", Inline: false},
			{Name: "`/subscriptions upcoming`", Value: "View subscriptions due for renewal within the next 30 days.", Inline: false},
			{Name: "`/subscriptions test-alert`", Value: "Trigger an immediate test DM alert to preview expiration notifications.", Inline: false},
			{Name: "`/subscriptions import`", Value: "Sync from `/home/daniel/Repos/_temp/Subscriptions.txt`.", Inline: false},
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

// handleSubList displays subscriptions with burn rate statistics.
func handleSubList(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	filter := "active"
	sortOrder := "renewal"
	for _, opt := range sub.Options {
		switch opt.Name {
		case "filter":
			filter = opt.StringValue()
		case "sort":
			sortOrder = opt.StringValue()
		}
	}

	subMu.Lock()
	items := make([]Subscription, len(subStore.Subscriptions))
	copy(items, subStore.Subscriptions)
	subMu.Unlock()

	now := time.Now()
	var filtered []Subscription

	for _, item := range items {
		switch filter {
		case "all":
			filtered = append(filtered, item)
		case "expiring":
			if item.Status == StatusActive && time.Until(item.NextRenewal) <= 7*24*time.Hour {
				filtered = append(filtered, item)
			}
		case "monthly":
			if item.BillingCycle == CycleMonthly && item.Status == StatusActive {
				filtered = append(filtered, item)
			}
		case "yearly":
			if item.BillingCycle == CycleYearly && item.Status == StatusActive {
				filtered = append(filtered, item)
			}
		case "cancelled":
			if item.Status == StatusCancelled {
				filtered = append(filtered, item)
			}
		default: // "active"
			if item.Status == StatusActive {
				filtered = append(filtered, item)
			}
		}
	}

	// Sort results
	switch sortOrder {
	case "cost_desc":
		sort.Slice(filtered, func(a, b int) bool { return filtered[a].Cost > filtered[b].Cost })
	case "cost_asc":
		sort.Slice(filtered, func(a, b int) bool { return filtered[a].Cost < filtered[b].Cost })
	case "name":
		sort.Slice(filtered, func(a, b int) bool { return strings.ToLower(filtered[a].Name) < strings.ToLower(filtered[b].Name) })
	default: // "renewal"
		sort.Slice(filtered, func(a, b int) bool { return filtered[a].NextRenewal.Before(filtered[b].NextRenewal) })
	}

	monthlyTotal, yearlyTotal := calculateBurnRates(items)

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("💳 Tracked Subscriptions (%d total)", len(filtered)),
		Color:       0x2563eb,
		Description: fmt.Sprintf("📊 **Monthly Burn Rate:** `$%.2f` | **Annual Spend:** `$%.2f`\nUse `/subscriptions renew id:<id>` once paid, or `/subscriptions modify` to edit.", monthlyTotal, yearlyTotal),
		Timestamp:   now.Format(time.RFC3339),
	}

	if len(filtered) == 0 {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "No Subscriptions Found",
			Value:  "No subscriptions matched the specified filter. Use `/subscriptions add` to track a new bill.",
			Inline: false,
		})
	} else {
		for idx, item := range filtered {
			if idx >= 15 {
				embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
					Name:   "...",
					Value:  fmt.Sprintf("*(and %d more items. Use filters to narrow down)*", len(filtered)-15),
					Inline: false,
				})
				break
			}

			statusBadge := "🟢 Active"
			if item.Status == StatusPaused {
				statusBadge = "⏸️ Paused"
			} else if item.Status == StatusCancelled {
				statusBadge = "❌ Cancelled"
			}

			daysUntil := int(math.Ceil(time.Until(item.NextRenewal).Hours() / 24))
			renewalDisplay := fmt.Sprintf("%s (<t:%d:R>)", item.NextRenewal.Format("Jan 02, 2006"), item.NextRenewal.Unix())
			if daysUntil <= 7 && daysUntil >= 0 && item.Status == StatusActive {
				renewalDisplay = fmt.Sprintf("⚠️ **%s**", renewalDisplay)
			} else if daysUntil < 0 && item.Status == StatusActive {
				renewalDisplay = fmt.Sprintf("🚨 **OVERDUE (%d days ago)**", -daysUntil)
			}

			fieldVal := fmt.Sprintf("💰 **$%.2f** / %s | %s\n📅 **Renews:** %s\n🆔 `%s`",
				item.Cost, item.BillingCycle, statusBadge, renewalDisplay, item.ID)
			if item.Category != "" {
				fieldVal += fmt.Sprintf(" | 🏷️ *%s*", item.Category)
			}
			if item.Notes != "" {
				fieldVal += fmt.Sprintf("\n📝 *%s*", item.Notes)
			}

			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   fmt.Sprintf("%s", item.Name),
				Value:  fieldVal,
				Inline: false,
			})
		}
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleSubAdd adds a new subscription.
func handleSubAdd(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	var name, costStr, renewalStr, cycleStr, category, notes string
	for _, opt := range sub.Options {
		switch opt.Name {
		case "name":
			name = strings.TrimSpace(opt.StringValue())
		case "cost":
			costStr = strings.TrimSpace(opt.StringValue())
		case "renewal":
			renewalStr = strings.TrimSpace(opt.StringValue())
		case "cycle":
			cycleStr = strings.TrimSpace(opt.StringValue())
		case "category":
			category = strings.TrimSpace(opt.StringValue())
		case "notes":
			notes = strings.TrimSpace(opt.StringValue())
		}
	}

	cost, err := parseCost(costStr)
	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Invalid cost %q: please supply a valid dollar amount like `19.99` or `$10.00`.", costStr))
		return
	}

	renewalDate, err := parseFlexibleDate(renewalStr)
	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Invalid renewal date %q: format as `YYYY-MM-DD` (e.g. `2027-05-06`), `May 6, 2027`, or relative like `in 30 days`.", renewalStr))
		return
	}

	cycle := BillingCycle(cycleStr)
	if cycle == "" {
		cycle = CycleMonthly
	}
	if category == "" {
		category = "Other"
	}

	invokerID := ""
	if i.Member != nil && i.Member.User != nil {
		invokerID = i.Member.User.ID
	} else if i.User != nil {
		invokerID = i.User.ID
	}

	subMu.Lock()
	id := generateSubscriptionID(name, subStore.Subscriptions)
	now := time.Now()
	newSub := Subscription{
		ID:           id,
		Name:         name,
		Cost:         cost,
		Currency:     "USD",
		BillingCycle: cycle,
		NextRenewal:  renewalDate,
		Category:     category,
		Status:       StatusActive,
		AutoRenew:    true,
		Notes:        notes,
		UserID:       invokerID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	subStore.Subscriptions = append(subStore.Subscriptions, newSub)
	err = saveSubscriptionStoreLocked()
	subMu.Unlock()

	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Failed to save subscription: %v", err))
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "✅ Subscription Tracked",
		Description: fmt.Sprintf("Successfully added **%s** to your recurring subscriptions.", name),
		Color:       0x10b981,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "🆔 ID", Value: fmt.Sprintf("`%s`", id), Inline: true},
			{Name: "💰 Cost", Value: fmt.Sprintf("$%.2f / %s", cost, cycle), Inline: true},
			{Name: "📅 Next Renewal", Value: fmt.Sprintf("%s (<t:%d:R>)", renewalDate.Format("Mon, Jan 02, 2006"), renewalDate.Unix()), Inline: true},
			{Name: "🏷️ Category", Value: category, Inline: true},
			{Name: "🔔 7-Day Alert", Value: "Enabled (Automatic DM to your Discord user)", Inline: true},
		},
	}
	if notes != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "📝 Notes", Value: notes, Inline: false})
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleSubModify updates an existing subscription.
func handleSubModify(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	var targetID, newName, costStr, renewalStr, cycleStr, category, statusStr, notes string
	hasName, hasCost, hasRenewal, hasCycle, hasCategory, hasStatus, hasNotes := false, false, false, false, false, false, false

	for _, opt := range sub.Options {
		switch opt.Name {
		case "id":
			targetID = strings.TrimSpace(opt.StringValue())
		case "name":
			newName = strings.TrimSpace(opt.StringValue())
			hasName = true
		case "cost":
			costStr = strings.TrimSpace(opt.StringValue())
			hasCost = true
		case "renewal":
			renewalStr = strings.TrimSpace(opt.StringValue())
			hasRenewal = true
		case "cycle":
			cycleStr = strings.TrimSpace(opt.StringValue())
			hasCycle = true
		case "category":
			category = strings.TrimSpace(opt.StringValue())
			hasCategory = true
		case "status":
			statusStr = strings.TrimSpace(opt.StringValue())
			hasStatus = true
		case "notes":
			notes = strings.TrimSpace(opt.StringValue())
			hasNotes = true
		}
	}

	var parsedCost float64
	if hasCost {
		c, err := parseCost(costStr)
		if err != nil {
			respondEphemeral(s, i, fmt.Sprintf("❌ Invalid cost %q: please supply a valid price like `19.99`.", costStr))
			return
		}
		parsedCost = c
	}

	var parsedRenewal time.Time
	if hasRenewal {
		r, err := parseFlexibleDate(renewalStr)
		if err != nil {
			respondEphemeral(s, i, fmt.Sprintf("❌ Invalid renewal date %q: use `YYYY-MM-DD`.", renewalStr))
			return
		}
		parsedRenewal = r
	}

	subMu.Lock()
	idx := findSubscriptionIndex(targetID, subStore.Subscriptions)
	if idx == -1 {
		subMu.Unlock()
		respondEphemeral(s, i, fmt.Sprintf("❌ Subscription matching %q was not found. Use `/subscriptions list` to check IDs.", targetID))
		return
	}

	curr := &subStore.Subscriptions[idx]
	if hasName {
		curr.Name = newName
	}
	if hasCost {
		curr.Cost = parsedCost
	}
	if hasRenewal {
		curr.NextRenewal = parsedRenewal
		// Clear last notified if date moved forward
		curr.LastNotified = time.Time{}
	}
	if hasCycle {
		curr.BillingCycle = BillingCycle(cycleStr)
	}
	if hasCategory {
		curr.Category = category
	}
	if hasStatus {
		curr.Status = SubscriptionStatus(statusStr)
	}
	if hasNotes {
		curr.Notes = notes
	}
	curr.UpdatedAt = time.Now()

	updatedItem := *curr
	err := saveSubscriptionStoreLocked()
	subMu.Unlock()

	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Failed to update subscription: %v", err))
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "✏️ Subscription Updated",
		Description: fmt.Sprintf("Successfully modified **%s** (`%s`).", updatedItem.Name, updatedItem.ID),
		Color:       0x06b6d4,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "💰 Cost", Value: fmt.Sprintf("$%.2f / %s", updatedItem.Cost, updatedItem.BillingCycle), Inline: true},
			{Name: "📅 Next Renewal", Value: fmt.Sprintf("%s (<t:%d:R>)", updatedItem.NextRenewal.Format("Mon, Jan 02, 2006"), updatedItem.NextRenewal.Unix()), Inline: true},
			{Name: "📊 Status", Value: string(updatedItem.Status), Inline: true},
			{Name: "🏷️ Category", Value: updatedItem.Category, Inline: true},
		},
	}
	if updatedItem.Notes != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "📝 Notes", Value: updatedItem.Notes, Inline: false})
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleSubDelete deletes a subscription by ID or name substring.
func handleSubDelete(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	var targetID string
	for _, opt := range sub.Options {
		if opt.Name == "id" {
			targetID = strings.TrimSpace(opt.StringValue())
		}
	}

	subMu.Lock()
	idx := findSubscriptionIndex(targetID, subStore.Subscriptions)
	if idx == -1 {
		subMu.Unlock()
		respondEphemeral(s, i, fmt.Sprintf("❌ Subscription matching %q was not found.", targetID))
		return
	}

	deleted := subStore.Subscriptions[idx]
	subStore.Subscriptions = append(subStore.Subscriptions[:idx], subStore.Subscriptions[idx+1:]...)
	err := saveSubscriptionStoreLocked()
	subMu.Unlock()

	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Failed to delete subscription: %v", err))
		return
	}

	respondEphemeral(s, i, fmt.Sprintf("🗑️ Successfully deleted subscription **%s** (`%s`).", deleted.Name, deleted.ID))
}

// handleSubRenew rolls a subscription's renewal date forward by one billing cycle.
func handleSubRenew(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	var targetID string
	for _, opt := range sub.Options {
		if opt.Name == "id" {
			targetID = strings.TrimSpace(opt.StringValue())
		}
	}

	subMu.Lock()
	idx := findSubscriptionIndex(targetID, subStore.Subscriptions)
	if idx == -1 {
		subMu.Unlock()
		respondEphemeral(s, i, fmt.Sprintf("❌ Subscription matching %q was not found.", targetID))
		return
	}

	curr := &subStore.Subscriptions[idx]
	oldDate := curr.NextRenewal
	newDate := advanceRenewalDate(oldDate, curr.BillingCycle)
	curr.NextRenewal = newDate
	curr.LastNotified = time.Time{} // Reset alert flag for the new period
	curr.Status = StatusActive
	curr.UpdatedAt = time.Now()

	renewed := *curr
	err := saveSubscriptionStoreLocked()
	subMu.Unlock()

	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Failed to update subscription renewal: %v", err))
		return
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🎉 Subscription Renewed",
		Description: fmt.Sprintf("Marked **%s** as paid and advanced its renewal schedule.", renewed.Name),
		Color:       0x10b981,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "⏮️ Previous Renewal", Value: oldDate.Format("Mon, Jan 02, 2006"), Inline: true},
			{Name: "⏭️ New Next Renewal", Value: fmt.Sprintf("%s (<t:%d:R>)", newDate.Format("Mon, Jan 02, 2006"), newDate.Unix()), Inline: true},
			{Name: "💰 Amount", Value: fmt.Sprintf("$%.2f / %s", renewed.Cost, renewed.BillingCycle), Inline: true},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "You will receive another automated DM reminder 7 days before this new renewal date.",
		},
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleSubStats displays a financial dashboard of subscriptions.
func handleSubStats(s *discordgo.Session, i *discordgo.InteractionCreate) {
	subMu.Lock()
	items := make([]Subscription, len(subStore.Subscriptions))
	copy(items, subStore.Subscriptions)
	subMu.Unlock()

	monthlyTotal, yearlyTotal := calculateBurnRates(items)

	categoryCosts := make(map[string]float64)
	activeCount := 0
	pausedCount := 0
	cancelledCount := 0
	expiringCount := 0
	now := time.Now()

	for _, item := range items {
		switch item.Status {
		case StatusActive:
			activeCount++
			mCost := toMonthlyCost(item.Cost, item.BillingCycle)
			categoryCosts[item.Category] += mCost
			if time.Until(item.NextRenewal) <= 7*24*time.Hour && time.Until(item.NextRenewal) > 0 {
				expiringCount++
			}
		case StatusPaused:
			pausedCount++
		case StatusCancelled:
			cancelledCount++
		}
	}

	var catLines []string
	for cat, cost := range categoryCosts {
		pct := 0.0
		if monthlyTotal > 0 {
			pct = (cost / monthlyTotal) * 100
		}
		catLines = append(catLines, fmt.Sprintf("• **%s:** `$%.2f/mo` (%.1f%%)", cat, cost, pct))
	}
	sort.Strings(catLines)

	embed := &discordgo.MessageEmbed{
		Title:       "📊 Subscription Financial Summary & Burn Rate",
		Color:       0x8b5cf6, // Violet
		Description: "Overview of your recurring commitments and projected expenditures.",
		Fields: []*discordgo.MessageEmbedField{
			{Name: "💸 Monthly Burn Rate", Value: fmt.Sprintf("**$%.2f** / month", monthlyTotal), Inline: true},
			{Name: "📅 Annual Projection", Value: fmt.Sprintf("**$%.2f** / year", yearlyTotal), Inline: true},
			{Name: "🔢 Active Services", Value: fmt.Sprintf("%d active (%d expiring in ≤7d)", activeCount, expiringCount), Inline: true},
			{Name: "📂 Monthly Spend by Category", Value: strings.Join(catLines, "\n"), Inline: false},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Data calculated from active subscriptions in Sunfire database.",
		},
		Timestamp: now.Format(time.RFC3339),
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleSubUpcoming shows subscriptions renewing within 30 days.
func handleSubUpcoming(s *discordgo.Session, i *discordgo.InteractionCreate) {
	subMu.Lock()
	items := make([]Subscription, len(subStore.Subscriptions))
	copy(items, subStore.Subscriptions)
	subMu.Unlock()

	now := time.Now()
	var upcoming []Subscription
	for _, item := range items {
		if item.Status == StatusActive && time.Until(item.NextRenewal) <= 30*24*time.Hour && time.Until(item.NextRenewal) > -24*time.Hour {
			upcoming = append(upcoming, item)
		}
	}

	sort.Slice(upcoming, func(a, b int) bool { return upcoming[a].NextRenewal.Before(upcoming[b].NextRenewal) })

	embed := &discordgo.MessageEmbed{
		Title:       "⏰ Upcoming Renewals (Next 30 Days)",
		Color:       0xf59e0b,
		Description: fmt.Sprintf("Found **%d** upcoming renewal(s) due within the next month.", len(upcoming)),
		Timestamp:   now.Format(time.RFC3339),
	}

	if len(upcoming) == 0 {
		embed.Description = "✅ **Clear horizon:** No subscriptions are due for renewal in the next 30 days."
	} else {
		for _, item := range upcoming {
			days := int(math.Ceil(time.Until(item.NextRenewal).Hours() / 24))
			urgency := "🟢"
			if days <= 7 {
				urgency = "⚠️"
			}
			if days <= 1 {
				urgency = "🚨"
			}

			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name: fmt.Sprintf("%s %s — $%.2f (%s)", urgency, item.Name, item.Cost, item.BillingCycle),
				Value: fmt.Sprintf("📅 **Due:** %s (<t:%d:R>)\n🆔 `%s` | Use `/subscriptions renew id:%s` once paid",
					item.NextRenewal.Format("Monday, Jan 02"), item.NextRenewal.Unix(), item.ID, item.ID),
				Inline: false,
			})
		}
	}

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// handleSubTestAlert triggers a test DM to the caller.
func handleSubTestAlert(s *discordgo.Session, i *discordgo.InteractionCreate) {
	invokerID := ""
	if i.Member != nil && i.Member.User != nil {
		invokerID = i.Member.User.ID
	} else if i.User != nil {
		invokerID = i.User.ID
	}

	if invokerID == "" {
		respondEphemeral(s, i, "❌ Unable to resolve your Discord User ID.")
		return
	}

	subMu.Lock()
	items := make([]Subscription, len(subStore.Subscriptions))
	copy(items, subStore.Subscriptions)
	subMu.Unlock()

	// Pick first active or mock one
	var sampleSub Subscription
	if len(items) > 0 {
		sampleSub = items[0]
	} else {
		sampleSub = Subscription{
			ID:           "sample-service",
			Name:         "Sample Service Pro",
			Cost:         19.99,
			Currency:     "USD",
			BillingCycle: CycleMonthly,
			NextRenewal:  time.Now().AddDate(0, 0, 5),
			Category:     "Productivity",
			Status:       StatusActive,
		}
	}

	err := deliverSubscriptionAlert(s, invokerID, sampleSub, true)
	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Failed to deliver test DM: %v\nEnsure your Discord privacy settings allow direct messages from server members.", err))
		return
	}

	respondEphemeral(s, i, "📬 **Test alert dispatched!** Check your Direct Messages from Sunfire.")
}

// handleSubImport imports entries from Subscriptions.txt.
func handleSubImport(s *discordgo.Session, i *discordgo.InteractionCreate) {
	filePath := "/home/daniel/Repos/_temp/Subscriptions.txt"
	imported, err := importSubscriptionsFromFile(filePath)
	if err != nil {
		respondEphemeral(s, i, fmt.Sprintf("❌ Failed to import %s: %v", filePath, err))
		return
	}

	subMu.Lock()
	countAdded := 0
	for _, item := range imported {
		idx := findSubscriptionIndex(item.ID, subStore.Subscriptions)
		if idx == -1 {
			subStore.Subscriptions = append(subStore.Subscriptions, item)
			countAdded++
		} else {
			// Update existing
			subStore.Subscriptions[idx].Cost = item.Cost
			subStore.Subscriptions[idx].NextRenewal = item.NextRenewal
			subStore.Subscriptions[idx].Notes = item.Notes
			subStore.Subscriptions[idx].UpdatedAt = time.Now()
		}
	}
	_ = saveSubscriptionStoreLocked()
	subMu.Unlock()

	respondEphemeral(s, i, fmt.Sprintf("✅ **Sync complete!** Imported %d new subscription(s) from `%s` (total tracked: %d).", countAdded, filePath, len(subStore.Subscriptions)))
}

// handleSubscriptionComponent handles interactive buttons like Renew and Cancel from DMs.
func handleSubscriptionComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	initSubscriptionStore()
	customID := i.MessageComponentData().CustomID

	if strings.HasPrefix(customID, "sub:renew:") {
		targetID := strings.TrimPrefix(customID, "sub:renew:")
		subMu.Lock()
		idx := findSubscriptionIndex(targetID, subStore.Subscriptions)
		if idx == -1 {
			subMu.Unlock()
			respondComponent(s, i, fmt.Sprintf("❌ Subscription `%s` not found.", targetID))
			return
		}
		curr := &subStore.Subscriptions[idx]
		curr.NextRenewal = advanceRenewalDate(curr.NextRenewal, curr.BillingCycle)
		curr.LastNotified = time.Time{}
		curr.Status = StatusActive
		curr.UpdatedAt = time.Now()
		name := curr.Name
		nextDate := curr.NextRenewal
		_ = saveSubscriptionStoreLocked()
		subMu.Unlock()

		respondComponent(s, i, fmt.Sprintf("✅ **Renewed!** Marked **%s** as paid. Next renewal is %s (<t:%d:R>).", name, nextDate.Format("Mon, Jan 02, 2006"), nextDate.Unix()))
		return
	}

	if strings.HasPrefix(customID, "sub:cancel:") {
		targetID := strings.TrimPrefix(customID, "sub:cancel:")
		subMu.Lock()
		idx := findSubscriptionIndex(targetID, subStore.Subscriptions)
		if idx == -1 {
			subMu.Unlock()
			respondComponent(s, i, fmt.Sprintf("❌ Subscription `%s` not found.", targetID))
			return
		}
		subStore.Subscriptions[idx].Status = StatusCancelled
		subStore.Subscriptions[idx].UpdatedAt = time.Now()
		name := subStore.Subscriptions[idx].Name
		_ = saveSubscriptionStoreLocked()
		subMu.Unlock()

		respondComponent(s, i, fmt.Sprintf("🛑 Marked **%s** as **Cancelled**. You will no longer receive renewal alerts for it.", name))
		return
	}
}

// -----------------------------------------------------------------------------
// Background Expiration DM Alert Daemon
// -----------------------------------------------------------------------------

// startSubscriptionsNotifierDaemon runs periodically, scanning for subscriptions
// within 7 days of expiration, and sending Direct Messages to the owner or assigned user.
func startSubscriptionsNotifierDaemon(ctx context.Context, s *discordgo.Session) {
	initSubscriptionStore()
	log.Println("Subscriptions: notifier daemon started (checking renewals every 1h)")

	// Initial check after 30 seconds, then hourly
	runPeriodic(ctx, 30*time.Second, 1*time.Hour, func() {
		checkAndDeliverSubscriptionAlerts(s)
	})
}

func checkAndDeliverSubscriptionAlerts(s *discordgo.Session) {
	cfg, err := loadConfig()
	ownerID := ""
	if err == nil && cfg != nil && cfg.OwnerID != "" && cfg.OwnerID != "PASTE_YOUR_DISCORD_USER_ID_HERE" {
		ownerID = cfg.OwnerID
	}

	subMu.Lock()
	now := time.Now()
	var toAlert []Subscription

	for i := range subStore.Subscriptions {
		sub := &subStore.Subscriptions[i]
		if sub.Status != StatusActive {
			continue
		}

		timeUntil := time.Until(sub.NextRenewal)
		// Check if renewal is within 7 days and has not been overdue for more than 2 days
		if timeUntil <= 7*24*time.Hour && timeUntil > -48*time.Hour {
			// Throttle alerts: only send once per 24 hours per subscription
			if now.Sub(sub.LastNotified) >= 24*time.Hour {
				sub.LastNotified = now
				toAlert = append(toAlert, *sub)
			}
		}
	}

	if len(toAlert) > 0 {
		_ = saveSubscriptionStoreLocked()
	}
	subMu.Unlock()

	for _, sub := range toAlert {
		targetUserID := sub.UserID
		if targetUserID == "" {
			targetUserID = ownerID
		}
		if targetUserID == "" {
			log.Printf("Subscriptions: alert for %s skipped (no target user ID or owner ID configured)", sub.Name)
			continue
		}

		if err := deliverSubscriptionAlert(s, targetUserID, sub, false); err != nil {
			log.Printf("Subscriptions: failed to DM alert for %s to %s: %v", sub.Name, targetUserID, err)
		} else {
			log.Printf("Subscriptions: delivered 7-day renewal DM alert for %s to %s", sub.Name, targetUserID)
		}
	}
}

// deliverSubscriptionAlert formats and sends a Discord DM with interactive buttons.
func deliverSubscriptionAlert(s *discordgo.Session, userID string, sub Subscription, isTest bool) error {
	dmCh, err := s.UserChannelCreate(userID)
	if err != nil {
		return fmt.Errorf("creating DM channel: %w", err)
	}

	daysRemaining := int(math.Ceil(time.Until(sub.NextRenewal).Hours() / 24))
	urgencyTitle := fmt.Sprintf("🔔 Subscription Expiration Alert: %s", sub.Name)
	urgencyColor := 0xf59e0b // Amber

	if daysRemaining <= 1 {
		urgencyTitle = fmt.Sprintf("🚨 URGENT: %s Renews Today / Tomorrow!", sub.Name)
		urgencyColor = 0xef4444 // Red
	} else if daysRemaining < 0 {
		urgencyTitle = fmt.Sprintf("⚠️ %s Is Overdue for Renewal", sub.Name)
		urgencyColor = 0xb91c1c
	}

	desc := fmt.Sprintf("Your subscription to **%s** is scheduled to renew in **%d day(s)** on **%s** (<t:%d:R>).",
		sub.Name, daysRemaining, sub.NextRenewal.Format("Monday, Jan 02, 2006"), sub.NextRenewal.Unix())
	if isTest {
		desc = "*(This is a test notification triggered via `/subscriptions test-alert`)*\n\n" + desc
	}

	embed := &discordgo.MessageEmbed{
		Title:       urgencyTitle,
		Description: desc,
		Color:       urgencyColor,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "💰 Renewal Cost", Value: fmt.Sprintf("**$%.2f** / %s", sub.Cost, sub.BillingCycle), Inline: true},
			{Name: "📅 Renewal Date", Value: fmt.Sprintf("%s\n(<t:%d:D>)", sub.NextRenewal.Format("Jan 02, 2006"), sub.NextRenewal.Unix()), Inline: true},
			{Name: "🏷️ Category", Value: sub.Category, Inline: true},
			{Name: "🆔 Subscription ID", Value: fmt.Sprintf("`%s`", sub.ID), Inline: true},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Click [Mark as Renewed] below to advance the cycle, or [Cancel] to stop tracking.",
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if sub.Notes != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "📝 Notes", Value: sub.Notes, Inline: false})
	}

	// Interactive component buttons in DM
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "✅ Mark as Renewed",
					Style:    discordgo.SuccessButton,
					CustomID: fmt.Sprintf("sub:renew:%s", sub.ID),
				},
				discordgo.Button{
					Label:    "🛑 Cancelled / Stop Tracking",
					Style:    discordgo.DangerButton,
					CustomID: fmt.Sprintf("sub:cancel:%s", sub.ID),
				},
			},
		},
	}

	_, err = s.ChannelMessageSendComplex(dmCh.ID, &discordgo.MessageSend{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	})
	return err
}

// -----------------------------------------------------------------------------
// Helper Utilities & Calculation Functions
// -----------------------------------------------------------------------------

// advanceRenewalDate rolls a renewal date forward by one billing cycle.
func advanceRenewalDate(current time.Time, cycle BillingCycle) time.Time {
	switch cycle {
	case CycleYearly:
		return current.AddDate(1, 0, 0)
	case CycleQuarterly:
		return current.AddDate(0, 3, 0)
	case CycleWeekly:
		return current.AddDate(0, 0, 7)
	case CycleLifetime:
		return current
	default: // CycleMonthly
		return current.AddDate(0, 1, 0)
	}
}

// toMonthlyCost converts any billing cycle cost to a normalized monthly figure.
func toMonthlyCost(cost float64, cycle BillingCycle) float64 {
	switch cycle {
	case CycleYearly:
		return cost / 12.0
	case CycleQuarterly:
		return cost / 3.0
	case CycleWeekly:
		return cost * (52.0 / 12.0)
	case CycleLifetime:
		return 0.0
	default:
		return cost
	}
}

// calculateBurnRates sums monthly and yearly expenditures.
func calculateBurnRates(subs []Subscription) (monthlyTotal float64, yearlyTotal float64) {
	for _, sub := range subs {
		if sub.Status != StatusActive {
			continue
		}
		mCost := toMonthlyCost(sub.Cost, sub.BillingCycle)
		monthlyTotal += mCost
		yearlyTotal += mCost * 12.0
	}
	return monthlyTotal, yearlyTotal
}

// findSubscriptionIndex searches by exact ID, case-insensitive ID, or name substring.
func findSubscriptionIndex(query string, subs []Subscription) int {
	cleanQuery := strings.ToLower(strings.TrimSpace(query))
	if cleanQuery == "" {
		return -1
	}

	// 1. Exact ID match
	for i, sub := range subs {
		if strings.ToLower(sub.ID) == cleanQuery {
			return i
		}
	}

	// 2. Exact Name match
	for i, sub := range subs {
		if strings.ToLower(sub.Name) == cleanQuery {
			return i
		}
	}

	// 3. Substring match
	var matches []int
	for i, sub := range subs {
		if strings.Contains(strings.ToLower(sub.ID), cleanQuery) || strings.Contains(strings.ToLower(sub.Name), cleanQuery) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}

	return -1
}

// generateSubscriptionID produces a unique slug identifier.
func generateSubscriptionID(name string, existing []Subscription) string {
	reg := regexp.MustCompile("[^a-z0-9]+")
	slug := reg.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 25 {
		slug = slug[:25]
	}
	if slug == "" {
		slug = "sub"
	}

	candidate := slug
	counter := 1
	for {
		taken := false
		for _, s := range existing {
			if s.ID == candidate {
				taken = true
				break
			}
		}
		if !taken {
			return candidate
		}
		counter++
		candidate = fmt.Sprintf("%s-%d", slug, counter)
	}
}

// parseCost converts strings like "19.99", "$19.99", "$19.99 USD" to a float64.
func parseCost(input string) (float64, error) {
	clean := strings.TrimSpace(input)
	clean = strings.ReplaceAll(clean, "$", "")
	clean = strings.ReplaceAll(clean, "USD", "")
	clean = strings.ReplaceAll(clean, "usd", "")
	clean = strings.TrimSpace(clean)

	val, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, err
	}
	if val < 0 {
		return 0, errors.New("cost cannot be negative")
	}
	return val, nil
}

// parseFlexibleDate parses various date formats.
func parseFlexibleDate(input string) (time.Time, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return time.Time{}, errors.New("empty date")
	}

	now := time.Now()
	lower := strings.ToLower(raw)

	// Relative offsets
	if lower == "today" {
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local), nil
	}
	if lower == "tomorrow" {
		t := now.AddDate(0, 0, 1)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local), nil
	}
	if strings.HasPrefix(lower, "in ") {
		rel := strings.TrimPrefix(lower, "in ")
		parts := strings.Fields(rel)
		if len(parts) >= 2 {
			count, err := strconv.Atoi(parts[0])
			if err == nil {
				unit := parts[1]
				switch {
				case strings.HasPrefix(unit, "day"):
					return now.AddDate(0, 0, count), nil
				case strings.HasPrefix(unit, "week"):
					return now.AddDate(0, 0, count*7), nil
				case strings.HasPrefix(unit, "month"):
					return now.AddDate(0, count, 0), nil
				case strings.HasPrefix(unit, "year"):
					return now.AddDate(count, 0, 0), nil
				}
			}
		}
	}

	formats := []string{
		"2006-01-02",
		"January 2, 2006",
		"January 02, 2006",
		"Jan 2, 2006",
		"Jan 02, 2006",
		"January 2 2006",
		"Jan 2 2006",
		"01/02/2006",
		"1/2/2006",
		"01-02-2006",
		"2006/01/02",
	}

	for _, layout := range formats {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("could not parse date %q", raw)
}

// importSubscriptionsFromFile reads /home/daniel/Repos/_temp/Subscriptions.txt.
func importSubscriptionsFromFile(path string) ([]Subscription, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var results []Subscription
	scanner := bufio.NewScanner(file)
	now := time.Now()

	costReg := regexp.MustCompile(`\$([0-9]+(?:\.[0-9]{2})?)`)
	dateReg := regexp.MustCompile(`(?i)(?:on\s+)?([A-Za-z]+\s+\d{1,2},?\s+\d{4})`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Clean up duplicate strings e.g. "Google AI Pro (5 TB)Google AI Pro (5 TB)"
		name := line
		cost := 0.0
		cycle := CycleMonthly
		category := "Other"
		renewalDate := now.AddDate(0, 1, 0)
		notes := ""

		if matches := costReg.FindStringSubmatch(line); len(matches) > 1 {
			if c, err := strconv.ParseFloat(matches[1], 64); err == nil {
				cost = c
			}
		}

		if strings.Contains(strings.ToLower(line), "year") || strings.Contains(strings.ToLower(line), "annual") {
			cycle = CycleYearly
		} else if strings.Contains(strings.ToLower(line), "week") {
			cycle = CycleWeekly
		}

		if dMatch := dateReg.FindStringSubmatch(line); len(dMatch) > 1 {
			if parsed, err := parseFlexibleDate(dMatch[1]); err == nil {
				renewalDate = parsed
			}
		}

		// Specialized parsing for known file lines
		if strings.Contains(line, "Google AI Pro") {
			name = "Google AI Pro (5 TB)"
			category = "AI"
			notes = "5 TB storage, $19.99 per month for 12 months"
			renewalDate = now.AddDate(0, 1, 0)
		} else if strings.Contains(line, "Microsoft 365") {
			name = "Microsoft 365 Premium"
			category = "Productivity"
			notes = "Next charge on May 6, 2027, $199.99 plus applicable taxes"
			cycle = CycleYearly
			renewalDate = time.Date(2027, time.May, 6, 0, 0, 0, 0, time.Local)
		} else if strings.Contains(line, "GitHub Copilot") {
			name = "GitHub Copilot Pro"
			category = "Developer Tools"
			notes = "$10 USD per month"
			cycle = CycleMonthly
			renewalDate = now.AddDate(0, 1, 0)
		} else {
			// Extract title before cost
			if loc := costReg.FindStringIndex(line); loc != nil && loc[0] > 0 {
				name = strings.TrimSpace(line[:loc[0]])
			}
		}

		results = append(results, Subscription{
			ID:           generateSubscriptionID(name, results),
			Name:         name,
			Cost:         cost,
			Currency:     "USD",
			BillingCycle: cycle,
			NextRenewal:  renewalDate,
			Category:     category,
			Status:       StatusActive,
			AutoRenew:    true,
			Notes:        notes,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: message,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func respondComponent(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: message,
		},
	})
}
