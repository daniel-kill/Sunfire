// lyrics.go — normalization of song-lyric text before it's sent to the Venice
// music API. The repo's song .md files (Tala/Ordo style) embed stage-direction
// and voice-tag headers like:
//
//	[Chorus - **Samael's voice, both registers simultaneously, inhuman**]
//
// Per the official MiniMax documentation, the lyrics input must contain ONLY the
// words to be sung plus clean structural tags; tags are "structural instructions
// rather than lines to be sung", and "lyrics define the words to be sung". Any
// descriptive text placed on a tag line (after the "]") gets treated as lyric
// content and sung/spoken by the model. So this normalizer:
//
//   - converts "[Chorus - **description**]" to a clean "[Chorus]" tag
//
//   - strips ALL descriptions from the lyrics (they belong in the prompt field)
//
//   - keeps only the sung words
//
//     [Chorus - **Samael's voice, both registers simultaneously, inhuman**]
//     ->  [Chorus]
package venice

import (
	"regexp"
	"strings"
)

// supportedTags are the section tags MiniMax Music v2.6 understands.
var supportedTags = map[string]bool{
	"intro": true, "verse": true, "pre-chorus": true, "chorus": true,
	"bridge": true, "outro": true, "hook": true, "solo": true,
	"break": true, "interlude": true, "instrumental": true, "drop": true,
	"build": true, "post-chorus": true,
}

// sectionHeaderRe matches a bracketed section header, optionally with a
// "- description" tail, e.g.:
//
//	[Chorus]                              -> group1="Chorus",     group2=""
//	[Chorus - Samael's voice...]          -> group1="Chorus",     group2=" - Samael's voice..."
//	[Verse 1 - **Male voice...**]         -> group1="Verse 1",    group2=" - **Male voice...**"
var sectionHeaderRe = regexp.MustCompile(`^\s*\[\s*([^\]\-]+?)\s*(\s*-\s*[^\]]*)?\]\s*$`)

// normalizeLyrics turns raw song-file text into clean, model-ready lyrics.
// It is safe to call with already-clean lyrics (returns them essentially as-is).
func normalizeLyrics(raw string) string {
	if raw == "" {
		return ""
	}

	lines := strings.Split(raw, "\n")
	var out []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Drop horizontal-rule separators and empty runs between sections.
		if trimmed == "" || trimmed == "---" || trimmed == "***" || trimmed == "___" {
			continue
		}

		// Drop explicit end marker.
		if strings.EqualFold(trimmed, "[End]") || strings.EqualFold(trimmed, "[END]") {
			continue
		}

		// Drop markdown headings (# 09 - Samael, ## Lyrics) — not lyric text.
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Section header: emit ONLY the clean tag ("[Verse]"). Descriptions after
		// the tag are dropped from the lyrics — MiniMax sings them otherwise.
		if m := sectionHeaderRe.FindStringSubmatch(trimmed); m != nil {
			tagWord := normalizeTagWord(m[1])
			if tagWord != "" {
				// Blank line separates the tag from the following lyrics block.
				out = append(out, "", "["+titleCaseTag(tagWord)+"]", "")
			}
			continue
		}

		// Ordinary lyric line: strip markdown emphasis markers but keep the text.
		clean := stripMarkdown(trimmed)
		if clean != "" {
			out = append(out, clean)
		}
	}

	// Join with newlines, then normalize spacing: keep a single blank line between
	// a section tag and its lyrics, but collapse any accidental double blanks and
	// trim leading/trailing blank lines.
	joined := strings.Join(out, "\n")
	joined = collapseBlankLines(joined)
	return strings.Trim(joined, "\n")
}

// collapseBlankLines reduces runs of two or more consecutive blank lines down to
// a single blank line, so tags and lyric blocks stay cleanly separated.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		if strings.TrimSpace(lines[i]) == "" {
			// Skip any following blank lines.
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}
		}
	}
	return strings.Join(out, "\n")
}

// normalizeTagWord maps a raw header word ("Chorus", "Verse 1", "Pre Chorus") to
// a canonical supported tag ("chorus", "verse", "pre-chorus"). Returns "" if the
// word isn't a recognized section (e.g. it was purely a description).
func normalizeTagWord(raw string) string {
	w := strings.ToLower(strings.TrimSpace(raw))
	// Strip trailing section numbers ("verse 1" -> "verse", "chorus 2" -> "chorus").
	w = strings.TrimRight(w, "0123456789 ")
	w = strings.TrimSpace(w)
	// Normalize spacing/hyphens for multi-word tags.
	replace := strings.NewReplacer("-", " ", "_", " ", "  ", " ")
	w = replace.Replace(w)
	w = strings.Join(strings.Fields(w), "-")

	// Accept "prechorus" / "postchorus" forms too.
	switch w {
	case "prechorus":
		w = "pre-chorus"
	case "postchorus":
		w = "post-chorus"
	}

	if supportedTags[w] {
		return w
	}
	return ""
}

// titleCaseTag capitalizes the first letter of each hyphen-separated word of a
// canonical tag so the output reads [Intro], [Pre-Chorus], [Outro] (the form the
// MiniMax models are documented with).
func titleCaseTag(tag string) string {
	parts := strings.Split(tag, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "-")
}

// stripMarkdown removes markdown emphasis ( * _ ` ) from a lyric line, keeping
// the words. Bold `**...**` and italic `*...*` both reduce to plain text.
var markdownRe = regexp.MustCompile(`[\\*_` + "`" + `]`)

func stripMarkdown(line string) string {
	s := markdownRe.ReplaceAllString(line, "")
	return strings.TrimSpace(s)
}
