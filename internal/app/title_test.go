package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTitleFromPrompt(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{"\n  \n# **修复分页**\n下面是正文", "修复分页"},
		{"- `go test`\n第二行", "go test"},
		{"* **更新文档**", "更新文档"},
		{"# **修复** `cursor` 的问题", "修复 cursor 的问题"},
		{"> ## `**混排 API**`", "混排 API"},
		{"\n\t\n plain title\nother", "plain title"},
		{"  \n\t", "未命名对话"},
		{"#\n\n正文标题", "正文标题"},
		{strings.Repeat("中", 21), strings.Repeat("中", 19) + "…"},
		{"API " + strings.Repeat("中", 19), "API " + strings.Repeat("中", 17) + "…"},
		{strings.Repeat("a", 40), strings.Repeat("a", 40)},
		{strings.Repeat("a", 41), strings.Repeat("a", 39) + "…"},
		{"\x1b[31m# `安全标题`\x1b[0m", "安全标题"},
	} {
		if got := TitleFromPrompt(tc.prompt); got != tc.want || ansi.StringWidth(got) > 40 {
			t.Errorf("%q: got %q (%d cols), want %q", tc.prompt, got, ansi.StringWidth(got), tc.want)
		}
	}
}
