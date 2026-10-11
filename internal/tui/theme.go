package tui

import "github.com/charmbracelet/lipgloss"

// Colors are the terminal tokens in docs/tui-demo.html.
type palette struct {
	term, bar, line, sel, code, sum, kbd lipgloss.Color
	ink, dim, faint, accent              lipgloss.Color
	run, ok, bad, badBG, wait, off, kw   lipgloss.Color
	addBG, delBG                         lipgloss.Color
}

func themePalette(theme string) palette {
	if theme == "light" {
		return palette{
			term: "#fbfcfd", bar: "#f1f4f7", line: "#d5dbe3", sel: "#e3ecfa", code: "#eef1f5", sum: "#f2f5f9", kbd: "#e2e7ee",
			ink: "#1d2430", dim: "#586273", faint: "#97a0ad", accent: "#2b67d1",
			run: "#0b8577", ok: "#2a8a43", bad: "#cc3434", badBG: "#fbe9e9", wait: "#a06d00", off: "#7a8390", kw: "#8143c9",
			addBG: "#e5f3e9", delBG: "#fbe8ea",
		}
	}
	return palette{
		term: "#11151a", bar: "#0d1014", line: "#262d36", sel: "#1a2330", code: "#171c23", sum: "#141a21", kbd: "#222a34",
		ink: "#d9dee6", dim: "#8a94a3", faint: "#525b69", accent: "#8fb8ff",
		run: "#5fd3c6", ok: "#7fcf8e", bad: "#ff7a7a", badBG: "#2a1518", wait: "#e0bf6e", off: "#6f7886", kw: "#c99cf0",
		addBG: "#13231a", delBG: "#26161a",
	}
}
func (p palette) text(color lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(color)
}
func (p palette) status(status string) lipgloss.Color {
	switch status {
	case "running":
		return p.run
	case "queued":
		return p.wait
	case "succeeded":
		return p.ok
	case "failed", "stale":
		return p.bad
	default:
		return p.off
	}
}
