package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// Fenced code is cut out before glamour runs and drawn separately: glamour has no
// block background for code, so a fence would read as plain indented text.
var (
	fenceOpen = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})\\s*([^`\\s]*)")
	token     = regexp.MustCompile(`BATONCODE(\d+)`)
)

type fence struct{ lang, code string }

func renderMarkdown(text string, width int, theme string) (string, error) {
	p := themePalette(theme)
	source, fences := cutFences(strings.ReplaceAll(text, "\t", "    "))
	r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle(p, theme)), glamour.WithWordWrap(width))
	if err != nil {
		return "", err
	}
	out, err := r.Render(source)
	if err != nil {
		return "", err
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if m := token.FindStringSubmatch(xansi.Strip(line)); m != nil {
			var n int
			fmt.Sscan(m[1], &n)
			if n < len(fences) {
				lines[i] = codeBlock(fences[n], p, width)
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// cutFences replaces each top-level fenced block with a one-word paragraph. An
// unclosed fence (a reply still streaming) runs to the end of the text.
func cutFences(text string) (string, []fence) {
	var out []string
	var fences []fence
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		m := fenceOpen.FindStringSubmatch(lines[i])
		if m == nil {
			out = append(out, lines[i])
			continue
		}
		marker, body := m[1], []string{}
		for i++; i < len(lines); i++ {
			if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, marker) && strings.Trim(t, marker[:1]) == "" {
				break
			}
			body = append(body, lines[i])
		}
		out = append(out, "", fmt.Sprintf("BATONCODE%d", len(fences)), "")
		fences = append(fences, fence{lang: m[2], code: strings.Join(body, "\n")})
	}
	return strings.Join(out, "\n"), fences
}

func codeBlock(f fence, p palette, width int) string {
	lexer := lexers.Get(f.lang)
	if lexer == nil {
		lexer = lexers.Analyse(f.code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	style := chroma.MustNewStyle("baton", chroma.StyleEntries{
		chroma.Text:          string(p.ink),
		chroma.Keyword:       string(p.kw),
		chroma.NameBuiltin:   string(p.kw),
		chroma.NameFunction:  string(p.accent),
		chroma.LiteralString: string(p.str),
		chroma.LiteralNumber: string(p.wait),
		chroma.Comment:       "italic " + string(p.faint),
		chroma.Operator:      string(p.dim),
		chroma.Punctuation:   string(p.dim),
	})
	block := p.text(p.ink).Background(p.code)
	w := max(1, width-4) // glamour's document margin is 2 on each side
	var rows []string
	if f.lang != "" {
		rows = append(rows, "  "+paint(p.text(p.faint).Render(" "+f.lang), block, w))
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, f.code)
	if err != nil {
		for _, line := range strings.Split(f.code, "\n") {
			rows = append(rows, "  "+paint(" "+line, block, w))
		}
		return strings.Join(rows, "\n")
	}
	for _, line := range chroma.SplitTokensIntoLines(it.Tokens()) {
		var b strings.Builder
		_ = formatters.TTY16m.Format(&b, style, chroma.Literator(line...))
		rows = append(rows, "  "+paint(" "+strings.TrimRight(b.String(), "\n"), block, w))
	}
	return strings.Join(rows, "\n")
}

// markdownStyle maps the Baton palette onto glamour's defaults: headings lose
// their literal "##" prefixes and differ by weight and color instead.
func markdownStyle(p palette, theme string) ansi.StyleConfig {
	s := styles.DarkStyleConfig
	if theme == "light" {
		s = styles.LightStyleConfig
	}
	c := func(col lipgloss.Color) *string { v := string(col); return &v }
	yes := func() *bool { v := true; return &v }
	s.Document.Color = c(p.ink)
	s.Text.Color = c(p.ink)
	s.Paragraph.Color = c(p.ink)
	s.Heading = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BlockSuffix: "\n", Color: c(p.ink), Bold: yes()}}
	s.H1 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: " ", Suffix: " ", Color: c(p.term), BackgroundColor: c(p.accent), Bold: yes()}}
	s.H2 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "▌ ", Color: c(p.accent), Bold: yes()}}
	s.H3 = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c(p.ink), Bold: yes()}}
	for _, h := range []*ansi.StyleBlock{&s.H4, &s.H5, &s.H6} {
		*h = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: c(p.dim), Bold: yes()}}
	}
	s.Strong = ansi.StylePrimitive{Color: c(p.strong), Bold: yes()}
	s.Code = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: " ", Suffix: " ", Color: c(p.mdCode), BackgroundColor: c(p.code)}}
	s.Item.Color = c(p.ink)
	s.Enumeration.Color = c(p.accent)
	s.Link = ansi.StylePrimitive{Color: c(p.accent), Underline: yes()}
	s.LinkText = ansi.StylePrimitive{Color: c(p.accent), Bold: yes()}
	s.BlockQuote.Color = c(p.dim)
	s.HorizontalRule.Color = c(p.line)
	s.Table.Color = c(p.ink)
	return s
}
