package index

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	idRe      = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-[0-9]+)\b`)
	mdLinkRe  = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	wikiRe    = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	statusRe  = regexp.MustCompile(`(?i)\bstatus\s*[:=]\s*([a-z0-9_-]+)|\[( |x|X)\]`)
)

func ParseSource(source SourceRecord) ParsedSource {
	normalized, diagnostics := normalizeBody(source)
	lineStarts := computeLineStarts(source.Body)
	frontmatter, bodyStart, fmDiagnostics := parseFrontmatter(source, normalized, lineStarts)
	diagnostics = append(diagnostics, fmDiagnostics...)
	parsed := ParsedSource{SourceID: source.ID, ContentHash: ContentHash(source.Body), Frontmatter: frontmatter, Diagnostics: diagnostics, LineStarts: lineStarts, NormalizedBody: normalized}
	metadata := mergeMetadata(source.Metadata, frontmatter.Values)
	parsed.Aliases = aliasesFromMetadata(metadata)
	lines := splitNormalizedLines(normalized)
	offset := 0
	var headingStack []Heading
	for lineIndex, line := range lines {
		lineNo := lineIndex + 1
		lineByteStart := normalizedOffsetToOriginalOffset(source.Body, normalized, offset)
		lineByteEnd := normalizedOffsetToOriginalOffset(source.Body, normalized, offset+len(line))
		if offset < bodyStart {
			offset += len(line) + newlineWidthAt(normalized, offset+len(line))
			continue
		}
		if match := headingRe.FindStringSubmatch(line); len(match) == 3 {
			level := len(match[1])
			title := strings.TrimSpace(match[2])
			headingStack = trimHeadingStack(headingStack, level)
			heading := Heading{Level: level, Title: title, ByteStart: lineByteStart, ByteEnd: lineByteEnd, LineStart: lineNo, LineEnd: lineNo}
			heading.HeadingPath = append(headingPath(headingStack), title)
			headingStack = append(headingStack, heading)
			parsed.Headings = append(parsed.Headings, heading)
		}
		path := headingPath(headingStack)
		for _, match := range mdLinkRe.FindAllStringSubmatchIndex(line, -1) {
			text := line[match[2]:match[3]]
			target := strings.TrimSpace(line[match[4]:match[5]])
			parsed.Links = append(parsed.Links, Link{Raw: line[match[0]:match[1]], Target: target, Text: text, Kind: linkKind(target), ByteStart: lineByteStart + match[0], ByteEnd: lineByteStart + match[1], LineStart: lineNo, LineEnd: lineNo})
		}
		for _, match := range wikiRe.FindAllStringSubmatchIndex(line, -1) {
			target := strings.TrimSpace(line[match[2]:match[3]])
			parsed.Links = append(parsed.Links, Link{Raw: line[match[0]:match[1]], Target: target, Text: target, Kind: "wiki", ByteStart: lineByteStart + match[0], ByteEnd: lineByteStart + match[1], LineStart: lineNo, LineEnd: lineNo})
		}
		for _, match := range idRe.FindAllStringSubmatchIndex(line, -1) {
			parsed.StableIDs = append(parsed.StableIDs, StableID{ID: line[match[2]:match[3]], ByteStart: lineByteStart + match[2], ByteEnd: lineByteStart + match[3], LineStart: lineNo, LineEnd: lineNo})
		}
		if match := statusRe.FindStringSubmatchIndex(line); len(match) > 0 {
			value := "open"
			if match[2] >= 0 {
				value = strings.ToLower(line[match[2]:match[3]])
			} else if strings.Contains(strings.ToLower(line[match[0]:match[1]]), "x") {
				value = "done"
			}
			parsed.Statuses = append(parsed.Statuses, Status{Value: value, ByteStart: lineByteStart + match[0], ByteEnd: lineByteStart + match[1], LineStart: lineNo, LineEnd: lineNo, HeadingPath: path})
		}
		offset += len(line) + newlineWidthAt(normalized, offset+len(line))
	}
	parsed.StableIDs = append(parsed.StableIDs, StableID{ID: source.ID, LineStart: 1, LineEnd: 1})
	return parsed
}

func parseFrontmatter(source SourceRecord, normalized string, lineStarts []int) (Frontmatter, int, []CollisionDiagnostic) {
	frontmatter := Frontmatter{Values: map[string]string{}, Valid: true}
	if !strings.HasPrefix(normalized, "---\n") && normalized != "---" {
		return frontmatter, 0, nil
	}
	end := strings.Index(normalized[4:], "\n---")
	if end < 0 {
		frontmatter.Valid = false
		return frontmatter, 0, []CollisionDiagnostic{{SourceID: source.ID, Kind: "malformed_frontmatter", Message: "frontmatter opener has no closing marker", LineStart: 1, LineEnd: 1}}
	}
	end += 4
	block := normalized[4:end]
	for i, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			frontmatter.Valid = false
			return frontmatter, end + len("\n---\n"), []CollisionDiagnostic{{SourceID: source.ID, Kind: "malformed_frontmatter", Message: "frontmatter line is not key/value", LineStart: i + 2, LineEnd: i + 2}}
		}
		frontmatter.Values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return frontmatter, end + len("\n---\n"), nil
}

func normalizeBody(source SourceRecord) (string, []CollisionDiagnostic) {
	body := source.Body
	var diagnostics []CollisionDiagnostic
	if !utf8.ValidString(body) {
		for i := 0; i < len(body); {
			r, size := utf8.DecodeRuneInString(body[i:])
			if r == utf8.RuneError && size == 1 {
				diagnostics = append(diagnostics, CollisionDiagnostic{SourceID: source.ID, Kind: "invalid_utf8", Message: "invalid UTF-8 byte replaced in normalized text", LineStart: lineForOffset(computeLineStarts(body), i), LineEnd: lineForOffset(computeLineStarts(body), i)})
			}
			i += size
		}
	}
	return strings.ReplaceAll(strings.ReplaceAll(strings.ToValidUTF8(body, "�"), "\r\n", "\n"), "\r", "\n"), diagnostics
}

func computeLineStarts(body string) []int {
	starts := []int{0}
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' {
			starts = append(starts, i+1)
		} else if body[i] == '\r' {
			if i+1 < len(body) && body[i+1] == '\n' {
				starts = append(starts, i+2)
				i++
			} else {
				starts = append(starts, i+1)
			}
		}
	}
	return starts
}

func lineForOffset(starts []int, offset int) int {
	idx := sort.Search(len(starts), func(i int) bool { return starts[i] > offset })
	if idx == 0 {
		return 1
	}
	return idx
}

func splitNormalizedLines(body string) []string {
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func newlineWidthAt(body string, offset int) int {
	if offset >= len(body) {
		return 0
	}
	if body[offset] == '\n' {
		return 1
	}
	return 0
}

func normalizedOffsetToOriginalOffset(original, normalized string, normalizedOffset int) int {
	oi, ni := 0, 0
	for oi < len(original) && ni < normalizedOffset {
		if original[oi] == '\r' {
			if oi+1 < len(original) && original[oi+1] == '\n' {
				oi += 2
			} else {
				oi++
			}
			ni++
			continue
		}
		_, osize := utf8.DecodeRuneInString(original[oi:])
		_, nsize := utf8.DecodeRuneInString(normalized[ni:])
		if osize < 1 {
			osize = 1
		}
		if nsize < 1 {
			nsize = 1
		}
		oi += osize
		ni += nsize
	}
	return oi
}

func trimHeadingStack(stack []Heading, level int) []Heading {
	for len(stack) > 0 && stack[len(stack)-1].Level >= level {
		stack = stack[:len(stack)-1]
	}
	return stack
}

func headingPath(stack []Heading) []string {
	path := make([]string, 0, len(stack))
	for _, heading := range stack {
		path = append(path, heading.Title)
	}
	return path
}

func linkKind(target string) string {
	if strings.HasPrefix(target, "#") {
		return "anchor"
	}
	if strings.Contains(target, ".md") {
		return "relative_path"
	}
	return "references"
}

func mergeMetadata(first, second map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range first {
		out[k] = v
	}
	for k, v := range second {
		out[k] = v
	}
	return out
}

func aliasesFromMetadata(metadata map[string]string) []Alias {
	seen := map[string]bool{}
	var aliases []Alias
	for _, key := range []string{"id", "alias", "aliases"} {
		value := metadata[key]
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			aliases = append(aliases, Alias{Type: "id", ID: part})
		}
	}
	return aliases
}
