package app

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// TitleFromPrompt also supplies display titles for tasks saved before Title existed.
func TitleFromPrompt(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(ansi.Strip(line))
		if line == "" {
			continue
		}
		for line != "" && strings.ContainsRune("#-*>", rune(line[0])) {
			if strings.HasPrefix(line, "**") && strings.Contains(line[2:], "**") {
				break
			}
			line = strings.TrimSpace(line[1:])
		}
		for _, mark := range []string{"`", "**"} {
			line = stripPairs(line, mark)
		}
		line = strings.TrimSpace(line)
		line = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, line)
		if line == "" {
			continue
		}
		return ansi.Truncate(line, 40, "…")
	}
	return "未命名对话"
}

func stripPairs(s, mark string) string {
	var out strings.Builder
	for {
		before, rest, found := strings.Cut(s, mark)
		inside, after, paired := strings.Cut(rest, mark)
		if !found || !paired {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(before)
		out.WriteString(inside)
		s = after
	}
}
