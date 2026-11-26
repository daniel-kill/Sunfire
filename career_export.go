package main

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ExportCareerStatusMarkdown generates an executive-grade Markdown document
// detailing the current pipeline status, filtered applications, action items,
// and certification roadmap.
func ExportCareerStatusMarkdown(apps []JobApplication, filter string, actionItems []CareerActionItem, certGoals []CertGoal) string {
	var sb strings.Builder
	now := time.Now()

	totalActive := 0
	totalApplied := 0
	totalInterviewing := 0
	totalSaved := 0
	totalOffer := 0
	totalRejected := 0
	totalDue := 0

	var dueApps []JobApplication
	for _, a := range apps {
		switch a.Status {
		case "Applied":
			totalApplied++
			totalActive++
		case "Interviewing":
			totalInterviewing++
			totalActive++
		case "Saved":
			totalSaved++
			totalActive++
		case "Offer":
			totalOffer++
			totalActive++
		case "Rejected":
			totalRejected++
		}

		isDue := !a.FollowedUp && now.After(a.FollowUpDate) && a.Status != "Rejected" && a.Status != "Archived"
		if isDue {
			totalDue++
			dueApps = append(dueApps, a)
		}
	}

	sb.WriteString("# 📊 Career Applications Pipeline — Status Report\n\n")
	sb.WriteString(fmt.Sprintf("> **Generated:** `%s`  \n", now.Format("Monday, January 02, 2006 at 15:04:05 MST")))
	sb.WriteString(fmt.Sprintf("> **Filter Applied:** `%s` (%d application(s) displayed)  \n", filter, len(apps)))
	sb.WriteString("> **Profile:** Daniel Kill • Toledo, OH (`daniel@danielkill.online`)  \n")
	sb.WriteString("> **System:** Sunfire Career Engine (`.private/career/career_store.json`)  \n\n")
	sb.WriteString("---\n\n")

	// 1. Executive Summary Table
	sb.WriteString("## 📈 Pipeline Executive Summary\n\n")
	sb.WriteString("| Metric | Count | Description |\n")
	sb.WriteString("| :--- | :---: | :--- |\n")
	sb.WriteString(fmt.Sprintf("| **Total Opportunities** | **%d** | All applications tracked in this view |\n", len(apps)))
	sb.WriteString(fmt.Sprintf("| **Active Pipeline** | **%d** | Ongoing opportunities (Applied, Interviewing, Saved, Offer) |\n", totalActive))
	sb.WriteString(fmt.Sprintf("| **Applications Submitted** | **%d** | Applications sent, awaiting response |\n", totalApplied))
	sb.WriteString(fmt.Sprintf("| **Active Interviews** | **%d** | In screening or technical interview stages |\n", totalInterviewing))
	sb.WriteString(fmt.Sprintf("| **Saved / Staged** | **%d** | Bookmarked opportunities preparing for submission |\n", totalSaved))
	if totalOffer > 0 {
		sb.WriteString(fmt.Sprintf("| **Offers Received** | **%d** | Offers extended |\n", totalOffer))
	}
	if totalRejected > 0 {
		sb.WriteString(fmt.Sprintf("| **Archived / Rejected** | **%d** | Concluded loops or closed listings |\n", totalRejected))
	}
	sb.WriteString(fmt.Sprintf("| **🔔 Follow-ups Due** | **%d** | Applications past 7-day scheduled follow-up window |\n\n", totalDue))
	sb.WriteString("---\n\n")

	// 2. Urgent Follow-up Section
	sb.WriteString("## 🔔 Follow-up Action Queue\n\n")
	if len(dueApps) > 0 {
		sb.WriteString(fmt.Sprintf("⚠️ **%d application(s) require follow-up check-in:**\n\n", len(dueApps)))
		for _, a := range dueApps {
			daysAgo := int(now.Sub(a.FollowUpDate).Hours() / 24)
			daysText := "due today"
			if daysAgo > 0 {
				daysText = fmt.Sprintf("%d day(s) overdue", daysAgo)
			}
			linkStr := ""
			if a.URL != "" {
				linkStr = fmt.Sprintf(" • [Listing](<%s>)", a.URL)
			}
			sb.WriteString(fmt.Sprintf("- 🔔 **%s** at **%s** (ID: `%s`%s)\n", a.Position, a.Organization, a.ID, linkStr))
			sb.WriteString(fmt.Sprintf("  - Applied: `%s` • Scheduled Follow-Up: `%s` (**%s**)\n",
				a.DateApplied.Format("Jan 02, 2006"), a.FollowUpDate.Format("Jan 02, 2006"), daysText))
			if a.Notes != "" {
				sb.WriteString(fmt.Sprintf("  - *Notes:* %s\n", a.Notes))
			}
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("✅ **All application follow-ups are up to date.** No pending recruiter check-ins required today.\n\n")
	}
	sb.WriteString("---\n\n")

	// 3. Complete Applications Table
	sb.WriteString(fmt.Sprintf("## 📋 Applications Directory (%d Entries)\n\n", len(apps)))
	if len(apps) == 0 {
		sb.WriteString("*No applications found matching the selected filter.*\n\n")
	} else {
		sb.WriteString("| # | Position | Organization | Status | Applied | Follow-Up Due | Followed Up? | Notes / Link |\n")
		sb.WriteString("| :-: | :--- | :--- | :-: | :-: | :-: | :-: | :--- |\n")

		// Sort by DateApplied descending
		sortedApps := make([]JobApplication, len(apps))
		copy(sortedApps, apps)
		sort.Slice(sortedApps, func(i, j int) bool {
			return sortedApps[i].DateApplied.After(sortedApps[j].DateApplied)
		})

		for idx, a := range sortedApps {
			statusBadge := a.Status
			switch a.Status {
			case "Interviewing":
				statusBadge = "🎯 Interviewing"
			case "Applied":
				statusBadge = "⏳ Applied"
			case "Saved":
				statusBadge = "📌 Saved"
			case "Offer":
				statusBadge = "🎉 Offer"
			case "Rejected":
				statusBadge = "❌ Rejected"
			}

			followedUpStr := "⏳ Pending"
			if a.FollowedUp {
				followedUpStr = "✅ Yes"
			}

			linkNote := ""
			if a.URL != "" {
				linkNote = fmt.Sprintf("[Job Link](<%s>)", a.URL)
			}
			if a.Notes != "" {
				if linkNote != "" {
					linkNote += " • "
				}
				cleanNote := strings.ReplaceAll(a.Notes, "|", "-")
				linkNote += cleanNote
			}
			if linkNote == "" {
				linkNote = "-"
			}

			sb.WriteString(fmt.Sprintf("| %d | **%s** | %s | %s | %s | %s | %s | %s |\n",
				idx+1,
				strings.ReplaceAll(a.Position, "|", "-"),
				strings.ReplaceAll(a.Organization, "|", "-"),
				statusBadge,
				a.DateApplied.Format("Jan 02, 2006"),
				a.FollowUpDate.Format("Jan 02, 2006"),
				followedUpStr,
				linkNote,
			))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("---\n\n")

	// 4. Checklist & Action Items from Jobs.txt
	if len(actionItems) > 0 {
		sb.WriteString(fmt.Sprintf("## 📝 Administrative Checklist & Action Items (`Jobs.txt` — %d Items)\n\n", len(actionItems)))
		for _, item := range actionItems {
			check := " "
			if strings.EqualFold(item.Status, "Completed") {
				check = "x"
			}
			noteStr := ""
			if item.Notes != "" {
				noteStr = fmt.Sprintf(" *(%s)*", item.Notes)
			}
			sb.WriteString(fmt.Sprintf("- [%s] **%s** (Category: `%s` • Status: `%s`)%s\n",
				check, item.Title, item.Category, item.Status, noteStr))
		}
		sb.WriteString("\n---\n\n")
	}

	// 5. Certification Roadmap from Certificate Planning.ods
	if len(certGoals) > 0 {
		sb.WriteString(fmt.Sprintf("## 🎓 Certification Roadmap & Study Targets (`Certificate Planning.ods` — %d Targets)\n\n", len(certGoals)))
		sb.WriteString("| Priority | Certification | Provider | Status | Strategic Relevance |\n")
		sb.WriteString("| :-: | :--- | :--- | :-: | :--- |\n")
		for _, g := range certGoals {
			progLink := g.Program
			if g.URL != "" {
				progLink = fmt.Sprintf("[%s](<%s>)", g.Program, g.URL)
			}
			sb.WriteString(fmt.Sprintf("| **%s** | %s | %s | `%s` | %s |\n",
				g.Priority, progLink, g.Provider, g.Status, g.Relevance))
		}
		sb.WriteString("\n---\n\n")
	}

	sb.WriteString("*Report generated automatically by Sunfire Discord Bot Career Copilot.*  \n")
	return sb.String()
}

// PDF Generation Types & Helper

type pdfPage struct {
	content bytes.Buffer
}

type pdfDocument struct {
	pages       []*pdfPage
	currentPage *pdfPage
	width       float64
	height      float64
}

func newPDFDocument() *pdfDocument {
	doc := &pdfDocument{
		width:  612, // Letter width
		height: 792, // Letter height
	}
	doc.newPage()
	return doc
}

func (doc *pdfDocument) newPage() {
	page := &pdfPage{}
	doc.pages = append(doc.pages, page)
	doc.currentPage = page
}

func sanitizePDFText(s string) string {
	// Map Unicode / Emojis to clean ASCII equivalents for standard Helvetica font
	replacer := strings.NewReplacer(
		"•", "*",
		"—", "-",
		"–", "-",
		"“", "\"",
		"”", "\"",
		"‘", "'",
		"’", "'",
		"✅", "[OK]",
		"⏳", "[Pending]",
		"🎯", "[Interview]",
		"🎉", "[Offer]",
		"❌", "[X]",
		"📌", "[Saved]",
		"🔔", "[DUE]",
		"⚠️", "[!]",
		"\r\n", " ",
		"\n", " ",
		"\r", " ",
		"\t", " ",
	)
	clean := replacer.Replace(s)

	var sb strings.Builder
	for _, r := range clean {
		if r >= 32 && r <= 126 {
			if r == '(' || r == ')' || r == '\\' {
				sb.WriteByte('\\')
			}
			sb.WriteRune(r)
		} else if r == ' ' {
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

func (doc *pdfDocument) drawRect(x, y, w, h float64, fillR, fillG, fillB float64, strokeR, strokeG, strokeB float64, lineWidth float64, doFill, doStroke bool) {
	p := doc.currentPage
	if doFill {
		fmt.Fprintf(&p.content, "%.3f %.3f %.3f rg\n", fillR, fillG, fillB)
	}
	if doStroke {
		fmt.Fprintf(&p.content, "%.3f %.3f %.3f RG\n", strokeR, strokeG, strokeB)
		fmt.Fprintf(&p.content, "%.2f w\n", lineWidth)
	}
	fmt.Fprintf(&p.content, "%.2f %.2f %.2f %.2f re\n", x, y, w, h)
	if doFill && doStroke {
		p.content.WriteString("B\n")
	} else if doFill {
		p.content.WriteString("f\n")
	} else if doStroke {
		p.content.WriteString("S\n")
	}
}

func (doc *pdfDocument) drawLine(x1, y1, x2, y2 float64, r, g, b float64, lineWidth float64) {
	p := doc.currentPage
	fmt.Fprintf(&p.content, "%.3f %.3f %.3f RG\n", r, g, b)
	fmt.Fprintf(&p.content, "%.2f w\n", lineWidth)
	fmt.Fprintf(&p.content, "%.2f %.2f m %.2f %.2f l S\n", x1, y1, x2, y2)
}

func (doc *pdfDocument) drawText(x, y float64, fontTag string, size float64, r, g, b float64, text string) {
	clean := sanitizePDFText(text)
	if len(clean) == 0 {
		return
	}
	p := doc.currentPage
	fmt.Fprintf(&p.content, "BT\n/%s %.1f Tf\n%.3f %.3f %.3f rg\n1 0 0 1 %.2f %.2f Tm\n(%s) Tj\nET\n",
		fontTag, size, r, g, b, x, y, clean)
}

func (doc *pdfDocument) buildBytes() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")

	numPages := len(doc.pages)
	// Object IDs:
	// 1: Catalog
	// 2: Pages
	// 3 .. 3+numPages-1: Page objects
	// Fonts:
	// F1 (Helvetica): 3 + numPages
	// F2 (Helvetica-Bold): 3 + numPages + 1
	// F3 (Helvetica-Oblique): 3 + numPages + 2
	// Streams: (3 + numPages + 3) .. (3 + 2*numPages + 2)

	fontF1 := 3 + numPages
	fontF2 := fontF1 + 1
	fontF3 := fontF1 + 2
	firstStreamObj := fontF3 + 1
	totalObjects := firstStreamObj + numPages

	offsets := make([]int, totalObjects)

	// 1 0 obj: Catalog
	offsets[1] = buf.Len()
	buf.WriteString("1 0 obj\n<<\n  /Type /Catalog\n  /Pages 2 0 R\n>>\nendobj\n")

	// 2 0 obj: Pages
	offsets[2] = buf.Len()
	buf.WriteString("2 0 obj\n<<\n  /Type /Pages\n  /Kids [")
	for i := 0; i < numPages; i++ {
		pageObjID := 3 + i
		buf.WriteString(fmt.Sprintf("%d 0 R ", pageObjID))
	}
	buf.WriteString(fmt.Sprintf("]\n  /Count %d\n>>\nendobj\n", numPages))

	// Page objects (3 .. 3+numPages-1)
	for i := 0; i < numPages; i++ {
		pageObjID := 3 + i
		streamObjID := firstStreamObj + i
		offsets[pageObjID] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<<\n  /Type /Page\n  /Parent 2 0 R\n  /MediaBox [0 0 %.1f %.1f]\n",
			pageObjID, doc.width, doc.height)
		fmt.Fprintf(&buf, "  /Resources <<\n    /Font <<\n      /F1 %d 0 R\n      /F2 %d 0 R\n      /F3 %d 0 R\n    >>\n  >>\n",
			fontF1, fontF2, fontF3)
		fmt.Fprintf(&buf, "  /Contents %d 0 R\n>>\nendobj\n", streamObjID)
	}

	// Font F1: Helvetica
	offsets[fontF1] = buf.Len()
	fmt.Fprintf(&buf, "%d 0 obj\n<<\n  /Type /Font\n  /Subtype /Type1\n  /BaseFont /Helvetica\n  /Encoding /WinAnsiEncoding\n>>\nendobj\n", fontF1)

	// Font F2: Helvetica-Bold
	offsets[fontF2] = buf.Len()
	fmt.Fprintf(&buf, "%d 0 obj\n<<\n  /Type /Font\n  /Subtype /Type1\n  /BaseFont /Helvetica-Bold\n  /Encoding /WinAnsiEncoding\n>>\nendobj\n", fontF2)

	// Font F3: Helvetica-Oblique
	offsets[fontF3] = buf.Len()
	fmt.Fprintf(&buf, "%d 0 obj\n<<\n  /Type /Font\n  /Subtype /Type1\n  /BaseFont /Helvetica-Oblique\n  /Encoding /WinAnsiEncoding\n>>\nendobj\n", fontF3)

	// Stream contents
	for i, p := range doc.pages {
		streamObjID := firstStreamObj + i
		contentBytes := p.content.Bytes()
		offsets[streamObjID] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<<\n  /Length %d\n>>\nstream\n", streamObjID, len(contentBytes))
		buf.Write(contentBytes)
		buf.WriteString("\nendstream\nendobj\n")
	}

	// XRef table
	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", totalObjects)
	buf.WriteString("0000000000 65535 f \r\n")
	for i := 1; i < totalObjects; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \r\n", offsets[i])
	}

	// Trailer
	fmt.Fprintf(&buf, "trailer\n<<\n  /Size %d\n  /Root 1 0 R\n>>\nstartxref\n%d\n%%%%EOF\n", totalObjects, xrefOffset)

	return buf.Bytes()
}

// ExportCareerStatusPDF renders a clean, styled, multi-page PDF document
// representing the application pipeline and KPI statistics.
func ExportCareerStatusPDF(apps []JobApplication, filter string, totalApplied, totalInterviewing, totalSaved, totalDue int) ([]byte, error) {
	doc := newPDFDocument()
	now := time.Now()

	// Sort applications: DateApplied descending
	sortedApps := make([]JobApplication, len(apps))
	copy(sortedApps, apps)
	sort.Slice(sortedApps, func(i, j int) bool {
		return sortedApps[i].DateApplied.After(sortedApps[j].DateApplied)
	})

	const (
		marginLeft   = 40.0
		contentWidth = 532.0
		marginTop    = 40.0
		pageHeight   = 792.0
	)

	drawHeader := func(isFirstPage bool) float64 {
		y := pageHeight - marginTop

		if isFirstPage {
			// Navy Blue Header Banner
			doc.drawRect(marginLeft, y-54, contentWidth, 54, 0.118, 0.227, 0.541, 0, 0, 0, 0, true, false)
			// Gold accent line
			doc.drawRect(marginLeft, y-57, contentWidth, 3, 0.961, 0.620, 0.043, 0, 0, 0, 0, true, false)

			// Banner Text
			doc.drawText(marginLeft+15, y-22, "F2", 15, 1, 1, 1, "SUNFIRE CAREER APPLICATIONS PIPELINE")
			subText := fmt.Sprintf("Pipeline Status Report  |  Filter: %s (%d items)  |  Generated: %s  |  Daniel Kill",
				filter, len(sortedApps), now.Format("Jan 02, 2006 15:04 MST"))
			doc.drawText(marginLeft+15, y-40, "F1", 8.5, 0.85, 0.90, 1.0, subText)

			y -= 70

			// KPI Summary Metric Cards (4 cards)
			cardW := 122.0
			cardGap := 14.6
			cardH := 38.0

			// Card 1: Active
			totalActive := totalApplied + totalInterviewing + totalSaved
			doc.drawRect(marginLeft, y-cardH, cardW, cardH, 0.937, 0.965, 1.0, 0.75, 0.85, 0.98, 0.8, true, true)
			doc.drawText(marginLeft+10, y-14, "F2", 7.5, 0.118, 0.314, 0.627, "ACTIVE PIPELINE")
			doc.drawText(marginLeft+10, y-30, "F2", 14, 0.118, 0.227, 0.541, fmt.Sprintf("%d Total", totalActive))

			// Card 2: Interviewing
			c2X := marginLeft + cardW + cardGap
			doc.drawRect(c2X, y-cardH, cardW, cardH, 0.980, 0.961, 1.0, 0.85, 0.75, 0.95, 0.8, true, true)
			doc.drawText(c2X+10, y-14, "F2", 7.5, 0.450, 0.150, 0.650, "INTERVIEWING")
			doc.drawText(c2X+10, y-30, "F2", 14, 0.500, 0.100, 0.700, fmt.Sprintf("%d Active", totalInterviewing))

			// Card 3: Applied
			c3X := c2X + cardW + cardGap
			doc.drawRect(c3X, y-cardH, cardW, cardH, 0.941, 0.992, 0.957, 0.75, 0.90, 0.80, 0.8, true, true)
			doc.drawText(c3X+10, y-14, "F2", 7.5, 0.080, 0.450, 0.200, "SUBMITTED")
			doc.drawText(c3X+10, y-30, "F2", 14, 0.090, 0.500, 0.220, fmt.Sprintf("%d Applied", totalApplied))

			// Card 4: Follow-ups Due
			c4X := c3X + cardW + cardGap
			cardDueBgR, cardDueBgG, cardDueBgB := 0.996, 0.949, 0.949
			cardDueTxR, cardDueTxG, cardDueTxB := 0.725, 0.110, 0.110
			if totalDue == 0 {
				cardDueBgR, cardDueBgG, cardDueBgB = 0.96, 0.97, 0.98
				cardDueTxR, cardDueTxG, cardDueTxB = 0.35, 0.40, 0.45
			}
			doc.drawRect(c4X, y-cardH, cardW, cardH, cardDueBgR, cardDueBgG, cardDueBgB, 0.90, 0.75, 0.75, 0.8, true, true)
			doc.drawText(c4X+10, y-14, "F2", 7.5, cardDueTxR, cardDueTxG, cardDueTxB, "FOLLOW-UPS DUE")
			doc.drawText(c4X+10, y-30, "F2", 14, cardDueTxR, cardDueTxG, cardDueTxB, fmt.Sprintf("%d Alerts", totalDue))

			y -= (cardH + 18)
		} else {
			// Compact Header on Subsequent Pages
			doc.drawRect(marginLeft, y-24, contentWidth, 24, 0.118, 0.227, 0.541, 0, 0, 0, 0, true, false)
			doc.drawText(marginLeft+12, y-16, "F2", 9, 1, 1, 1, "SUNFIRE CAREER APPLICATIONS PIPELINE (Continued)")
			doc.drawText(marginLeft+contentWidth-150, y-16, "F1", 8, 0.85, 0.90, 1.0, fmt.Sprintf("Filter: %s • %s", filter, now.Format("Jan 02, 2006")))
			y -= 34
		}

		// Table Header Row
		headerH := 18.0
		doc.drawRect(marginLeft, y-headerH, contentWidth, headerH, 0.925, 0.941, 0.961, 0.80, 0.83, 0.88, 0.8, true, true)
		doc.drawText(marginLeft+6, y-12, "F2", 8, 0.20, 0.25, 0.32, "#")
		doc.drawText(marginLeft+26, y-12, "F2", 8, 0.20, 0.25, 0.32, "POSITION & EMPLOYER")
		doc.drawText(marginLeft+230, y-12, "F2", 8, 0.20, 0.25, 0.32, "STATUS")
		doc.drawText(marginLeft+305, y-12, "F2", 8, 0.20, 0.25, 0.32, "APPLIED")
		doc.drawText(marginLeft+365, y-12, "F2", 8, 0.20, 0.25, 0.32, "FOLLOW-UP")
		doc.drawText(marginLeft+435, y-12, "F2", 8, 0.20, 0.25, 0.32, "NOTES / DETAILS")

		y -= headerH
		return y
	}

	curY := drawHeader(true)
	rowH := 24.0

	for idx, a := range sortedApps {
		// Page break check (leave 45pt for footer)
		if curY-rowH < 50 {
			doc.newPage()
			curY = drawHeader(false)
		}

		// Alternating row background
		if idx%2 == 1 {
			doc.drawRect(marginLeft, curY-rowH, contentWidth, rowH, 0.976, 0.984, 0.992, 0, 0, 0, 0, true, false)
		}

		// Row bottom separator line
		doc.drawLine(marginLeft, curY-rowH, marginLeft+contentWidth, curY-rowH, 0.89, 0.91, 0.94, 0.5)

		// Column 1: Index
		doc.drawText(marginLeft+6, curY-15, "F1", 7.5, 0.40, 0.45, 0.50, fmt.Sprintf("%d", idx+1))

		// Column 2: Position (Bold) & Organization (Regular)
		posStr := a.Position
		if len(posStr) > 34 {
			posStr = posStr[:32] + ".."
		}
		orgStr := a.Organization
		if len(orgStr) > 34 {
			orgStr = orgStr[:32] + ".."
		}
		doc.drawText(marginLeft+26, curY-10, "F2", 8, 0.12, 0.15, 0.20, posStr)
		doc.drawText(marginLeft+26, curY-20, "F1", 7, 0.40, 0.45, 0.52, orgStr)

		// Column 3: Status Badge
		badgeBgR, badgeBgG, badgeBgB := 0.92, 0.94, 0.96
		badgeTxR, badgeTxG, badgeTxB := 0.25, 0.30, 0.35
		switch a.Status {
		case "Interviewing":
			badgeBgR, badgeBgG, badgeBgB = 0.95, 0.90, 0.99
			badgeTxR, badgeTxG, badgeTxB = 0.45, 0.12, 0.65
		case "Applied":
			badgeBgR, badgeBgG, badgeBgB = 0.90, 0.95, 0.99
			badgeTxR, badgeTxG, badgeTxB = 0.10, 0.35, 0.65
		case "Saved":
			badgeBgR, badgeBgG, badgeBgB = 0.99, 0.96, 0.88
			badgeTxR, badgeTxG, badgeTxB = 0.65, 0.42, 0.05
		case "Offer":
			badgeBgR, badgeBgG, badgeBgB = 0.90, 0.98, 0.92
			badgeTxR, badgeTxG, badgeTxB = 0.10, 0.55, 0.20
		case "Rejected":
			badgeBgR, badgeBgG, badgeBgB = 0.98, 0.92, 0.92
			badgeTxR, badgeTxG, badgeTxB = 0.65, 0.20, 0.20
		}
		doc.drawRect(marginLeft+230, curY-18, 65, 13, badgeBgR, badgeBgG, badgeBgB, badgeTxR, badgeTxG, badgeTxB, 0.4, true, true)
		doc.drawText(marginLeft+234, curY-13.5, "F2", 6.5, badgeTxR, badgeTxG, badgeTxB, a.Status)

		// Column 4: Date Applied
		doc.drawText(marginLeft+305, curY-15, "F1", 7.5, 0.30, 0.35, 0.40, a.DateApplied.Format("Jan 02, 2006"))

		// Column 5: Follow-Up
		isDue := !a.FollowedUp && now.After(a.FollowUpDate) && a.Status != "Rejected" && a.Status != "Archived"
		if isDue {
			doc.drawText(marginLeft+365, curY-10, "F2", 7.5, 0.85, 0.12, 0.12, a.FollowUpDate.Format("Jan 02, 2006"))
			doc.drawText(marginLeft+365, curY-20, "F2", 6.5, 0.85, 0.12, 0.12, "[DUE NOW!]")
		} else if a.FollowedUp {
			doc.drawText(marginLeft+365, curY-10, "F1", 7.5, 0.30, 0.35, 0.40, a.FollowUpDate.Format("Jan 02, 2006"))
			doc.drawText(marginLeft+365, curY-20, "F1", 6.5, 0.10, 0.55, 0.20, "[Done [OK]]")
		} else {
			doc.drawText(marginLeft+365, curY-15, "F1", 7.5, 0.30, 0.35, 0.40, a.FollowUpDate.Format("Jan 02, 2006"))
		}

		// Column 6: Notes / Details
		notesStr := a.Notes
		if notesStr == "" && a.URL != "" {
			notesStr = a.URL
		}
		if len(notesStr) > 28 {
			notesStr = notesStr[:26] + ".."
		}
		if notesStr == "" {
			notesStr = "-"
		}
		doc.drawText(marginLeft+435, curY-15, "F3", 7, 0.45, 0.50, 0.55, notesStr)

		curY -= rowH
	}

	// Footers across all pages
	totalPageCount := len(doc.pages)
	for pageIdx, p := range doc.pages {
		doc.currentPage = p
		doc.drawLine(marginLeft, 38, marginLeft+contentWidth, 38, 0.82, 0.85, 0.88, 0.5)
		doc.drawText(marginLeft, 27, "F1", 7.5, 0.50, 0.55, 0.60,
			"Sunfire Job Search Copilot • .private/career/career_store.json")
		pageStr := fmt.Sprintf("Page %d of %d", pageIdx+1, totalPageCount)
		doc.drawText(marginLeft+contentWidth-50, 27, "F1", 7.5, 0.50, 0.55, 0.60, pageStr)
	}

	return doc.buildBytes(), nil
}
