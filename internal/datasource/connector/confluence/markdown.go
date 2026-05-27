package confluence

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	reCDATA           = regexp.MustCompile(`(?is)<!\[CDATA\[(.*?)\]\]>`)
	reTag             = regexp.MustCompile(`(?is)<[^>]+>`)
	reWhitespaceLines = regexp.MustCompile(`\n{3,}`)
)

func storageToMarkdown(storage string) string {
	if strings.TrimSpace(storage) == "" {
		return ""
	}
	content := handleConfluenceMacros(storage)

	for i := 6; i >= 1; i-- {
		re := regexp.MustCompile(`(?is)<h` + string(rune('0'+i)) + `[^>]*>(.*?)</h` + string(rune('0'+i)) + `>`)
		content = re.ReplaceAllStringFunc(content, func(match string) string {
			parts := re.FindStringSubmatch(match)
			if len(parts) < 2 {
				return ""
			}
			return strings.Repeat("#", i) + " " + stripTags(parts[1]) + "\n\n"
		})
	}

	content = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`).ReplaceAllString(content, "$1\n\n")
	content = regexp.MustCompile(`(?is)<(?:strong|b)[^>]*>(.*?)</(?:strong|b)>`).ReplaceAllString(content, "**$1**")
	content = regexp.MustCompile(`(?is)<(?:em|i)[^>]*>(.*?)</(?:em|i)>`).ReplaceAllString(content, "*$1*")
	content = regexp.MustCompile(`(?is)<code[^>]*>(.*?)</code>`).ReplaceAllString(content, "`$1`")

	content = regexp.MustCompile(`(?is)<a[^>]*href=["']([^"']*)["'][^>]*>(.*?)</a>`).ReplaceAllStringFunc(content, func(match string) string {
		m := regexp.MustCompile(`(?is)<a[^>]*href=["']([^"']*)["'][^>]*>(.*?)</a>`).FindStringSubmatch(match)
		if len(m) < 3 {
			return stripTags(match)
		}
		return "[" + stripTags(m[2]) + "](" + html.UnescapeString(m[1]) + ")"
	})

	content = convertUnorderedLists(content)
	content = convertOrderedLists(content)
	content = convertTables(content)

	content = regexp.MustCompile(`(?is)<br\s*/?>`).ReplaceAllString(content, "\n")
	content = regexp.MustCompile(`(?is)<hr\s*/?>`).ReplaceAllString(content, "\n---\n")
	content = regexp.MustCompile(`(?is)<blockquote[^>]*>(.*?)</blockquote>`).ReplaceAllStringFunc(content, func(match string) string {
		m := regexp.MustCompile(`(?is)<blockquote[^>]*>(.*?)</blockquote>`).FindStringSubmatch(match)
		if len(m) < 2 {
			return ""
		}
		text := stripTags(m[1])
		lines := strings.Split(text, "\n")
		for i := range lines {
			lines[i] = "> " + strings.TrimSpace(lines[i])
		}
		return strings.Join(lines, "\n") + "\n\n"
	})
	content = regexp.MustCompile(`(?is)<img[^>]*src=["']([^"']*)["'][^>]*alt=["']([^"']*)["'][^>]*>`).ReplaceAllString(content, "![$2]($1)")
	content = regexp.MustCompile(`(?is)<img[^>]*src=["']([^"']*)["'][^>]*>`).ReplaceAllString(content, "![image]($1)")

	content = stripTags(content)
	content = reWhitespaceLines.ReplaceAllString(content, "\n\n")
	return strings.TrimSpace(content) + "\n"
}

func handleConfluenceMacros(content string) string {
	content = replaceDiagramMacro(content, "drawio-sketch")
	content = replaceDiagramMacro(content, "drawio")
	content = replaceDiagramMacro(content, "gliffy")

	content = regexp.MustCompile(`(?is)<ac:image[^>]*>.*?</ac:image>`).ReplaceAllStringFunc(content, func(match string) string {
		name := attrValue(match, `ri:filename`)
		if name == "" {
			return ""
		}
		return "![" + name + "](attachments/" + name + ")"
	})

	content = regexp.MustCompile(`(?is)<ac:structured-macro[^>]*ac:name=["']code["'][^>]*>.*?<ac:plain-text-body>\s*<!\[CDATA\[(.*?)\]\]>\s*</ac:plain-text-body>\s*</ac:structured-macro>`).ReplaceAllString(content, "\n```\n$1\n```\n")

	for _, panel := range []string{"info", "note", "warning", "tip"} {
		re := regexp.MustCompile(`(?is)<ac:structured-macro[^>]*ac:name=["']` + panel + `["'][^>]*>.*?<ac:rich-text-body>(.*?)</ac:rich-text-body>\s*</ac:structured-macro>`)
		content = re.ReplaceAllString(content, "\n> **"+panel+":** $1\n")
	}

	content = regexp.MustCompile(`(?is)<ac:structured-macro[^>]*ac:name=["']status["'][^>]*>.*?<ac:parameter[^>]*ac:name=["']title["'][^>]*>(.*?)</ac:parameter>.*?</ac:structured-macro>`).ReplaceAllString(content, "[$1]")
	content = regexp.MustCompile(`(?is)<ac:structured-macro[^>]*>.*?</ac:structured-macro>`).ReplaceAllString(content, "")
	content = regexp.MustCompile(`(?is)<ri:user[^>]*ri:userkey=["'][^"']*["'][^>]*/>`).ReplaceAllString(content, "@user")
	content = regexp.MustCompile(`(?is)<ac:link[^>]*>.*?<ri:page[^>]*ri:content-title=["']([^"']*)["'][^>]*/>.*?</ac:link>`).ReplaceAllString(content, "[[$1]]")
	return content
}

func replaceDiagramMacro(content, name string) string {
	re := regexp.MustCompile(`(?is)<ac:structured-macro[^>]*ac:name=["']` + regexp.QuoteMeta(name) + `["'][^>]*>.*?</ac:structured-macro>`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		diagramName := parameterValue(match, "diagramName")
		if diagramName == "" {
			diagramName = "diagram"
		}
		return "\n![" + diagramName + "](attachments/" + diagramName + ".png)\n"
	})
}

func parameterValue(content, name string) string {
	re := regexp.MustCompile(`(?is)<ac:parameter[^>]*ac:name=["']` + regexp.QuoteMeta(name) + `["'][^>]*>(.*?)</ac:parameter>`)
	m := re.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(stripTags(m[1]))
}

func attrValue(content, attr string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(attr) + `=["']([^"']+)["']`)
	m := re.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return html.UnescapeString(m[1])
}

func convertUnorderedLists(content string) string {
	re := regexp.MustCompile(`(?is)<ul[^>]*>(.*?)</ul>`)
	itemRe := regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		m := re.FindStringSubmatch(match)
		if len(m) < 2 {
			return ""
		}
		var lines []string
		for _, item := range itemRe.FindAllStringSubmatch(m[1], -1) {
			lines = append(lines, "- "+strings.TrimSpace(stripTags(item[1])))
		}
		return strings.Join(lines, "\n") + "\n\n"
	})
}

func convertOrderedLists(content string) string {
	re := regexp.MustCompile(`(?is)<ol[^>]*>(.*?)</ol>`)
	itemRe := regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		m := re.FindStringSubmatch(match)
		if len(m) < 2 {
			return ""
		}
		var lines []string
		for i, item := range itemRe.FindAllStringSubmatch(m[1], -1) {
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(stripTags(item[1]))))
		}
		return strings.Join(lines, "\n") + "\n\n"
	})
}

func convertTables(content string) string {
	tableRe := regexp.MustCompile(`(?is)<table[^>]*>(.*?)</table>`)
	rowRe := regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	cellRe := regexp.MustCompile(`(?is)<t[hd][^>]*>(.*?)</t[hd]>`)
	return tableRe.ReplaceAllStringFunc(content, func(match string) string {
		m := tableRe.FindStringSubmatch(match)
		if len(m) < 2 {
			return ""
		}
		var rows []string
		for _, row := range rowRe.FindAllStringSubmatch(m[1], -1) {
			var cells []string
			for _, cell := range cellRe.FindAllStringSubmatch(row[1], -1) {
				cells = append(cells, strings.ReplaceAll(strings.TrimSpace(stripTags(cell[1])), "|", "\\|"))
			}
			if len(cells) == 0 {
				continue
			}
			rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
			if len(rows) == 1 {
				rows = append(rows, "| "+strings.Join(repeat("---", len(cells)), " | ")+" |")
			}
		}
		if len(rows) == 0 {
			return ""
		}
		return "\n" + strings.Join(rows, "\n") + "\n\n"
	})
}

func stripTags(text string) string {
	text = reCDATA.ReplaceAllString(text, "$1")
	text = reTag.ReplaceAllString(text, "")
	return html.UnescapeString(text)
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
