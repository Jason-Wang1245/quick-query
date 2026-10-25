package ui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"quick-query/internal/database"
)

var (
	accent        = lipgloss.Color("#6EDBD0")
	muted         = lipgloss.Color("#8694A8")
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("#274C58")).Foreground(lipgloss.Color("#FFFFFF"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle      = lipgloss.NewStyle().Foreground(muted)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF968F"))
)

// Database values are untrusted terminal text. Never render their ANSI/control
// sequences, including in the expanded viewer.
func safeText(s string, multiline bool) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' && multiline {
			return r
		}
		if r == '\n' {
			return '↵'
		}
		if r == '\t' {
			return '⇥'
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return '�'
		}
		return r
	}, s)
}

func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

type grid struct {
	result              database.Result
	row, col, top, left int
	width, height       int
	widths              []int
}

func newGrid(result database.Result) grid {
	g := grid{result: result}
	for col, name := range result.Columns {
		width := max(8, ansi.StringWidth(safeText(name, false)))
		for _, row := range result.Rows {
			if col < len(row) {
				width = max(width, min(32, ansi.StringWidth(safeText(row[col].Text, false))))
			}
		}
		g.widths = append(g.widths, min(32, width))
	}
	return g
}

func (g *grid) size(width, height int) {
	g.width = max(1, width)
	g.height = max(1, height)
	g.ensureVisible()
}
func (g *grid) pageSize() int           { return max(1, g.height-2) }
func (g *grid) columnWidth(col int) int { return min(g.widths[col], max(1, g.width-3)) }

func (g *grid) ensureVisible() {
	g.row = max(0, min(g.row, len(g.result.Rows)-1))
	g.col = max(0, min(g.col, len(g.result.Columns)-1))
	if g.row < g.top {
		g.top = g.row
	}
	if g.row >= g.top+g.pageSize() {
		g.top = g.row - g.pageSize() + 1
	}
	if g.col < g.left {
		g.left = g.col
	}
	for g.left < g.col {
		w := 0
		for c := g.left; c <= g.col; c++ {
			w += g.columnWidth(c) + 3
		}
		if w <= g.width {
			break
		}
		g.left++
	}
}

func (g *grid) navigate(key string) {
	switch key {
	case "up":
		g.row--
	case "down":
		g.row++
	case "left":
		g.col--
	case "right":
		g.col++
	case "pgup":
		g.row -= g.pageSize()
	case "pgdown":
		g.row += g.pageSize()
	case "home":
		g.col = 0
	case "end":
		g.col = len(g.result.Columns) - 1
	case "ctrl+home":
		g.row = 0
		g.col = 0
	case "ctrl+end":
		g.row = len(g.result.Rows) - 1
		g.col = len(g.result.Columns) - 1
	}
	g.ensureVisible()
}

func (g grid) cell() (database.Cell, bool) {
	if len(g.result.Rows) == 0 || len(g.result.Columns) == 0 {
		return database.Cell{}, false
	}
	return g.result.Rows[g.row][g.col], true
}

func (g grid) view(focused bool) string {
	if len(g.result.Columns) == 0 {
		command := g.result.Command
		if command == "" {
			command = "Statement completed."
		}
		return "\n  " + headerStyle.Render(safeText(command, false))
	}
	var lines []string
	renderRow := func(row []database.Cell, header bool, rowIndex int) string {
		var cells []string
		used := 0
		for col := g.left; col < len(g.result.Columns); col++ {
			w := g.columnWidth(col)
			if used+w+3 > g.width {
				break
			}
			text := ""
			if header {
				text = safeText(g.result.Columns[col], false)
			} else if row[col].Null {
				text = "NULL"
			} else {
				text = safeText(row[col].Text, false)
			}
			text = " " + fit(text, w) + " "
			if header {
				text = headerStyle.Render(text)
			} else if rowIndex == g.row && col == g.col {
				if focused {
					text = selectedStyle.Render(text)
				} else {
					text = lipgloss.NewStyle().Underline(true).Render(text)
				}
			} else if row[col].Null {
				text = dimStyle.Render(text)
			}
			cells = append(cells, text)
			used += w + 3
		}
		return strings.Join(cells, dimStyle.Render("│"))
	}
	lines = append(lines, renderRow(nil, true, 0))
	if len(g.result.Rows) == 0 {
		message := "  No rows returned."
		if g.result.Truncated {
			message = "  Rows exceeded the retention limit."
		}
		lines = append(lines, dimStyle.Render(message))
	}
	for i := g.top; i < len(g.result.Rows) && i < g.top+g.pageSize(); i++ {
		lines = append(lines, renderRow(g.result.Rows[i], false, i))
	}
	for len(lines) < g.height-1 {
		lines = append(lines, "")
	}
	summary := fmt.Sprintf(" %d rows", g.result.TotalRows)
	if len(g.result.Rows) > 0 {
		summary += fmt.Sprintf(" · row %d/%d · column %d/%d", g.row+1, len(g.result.Rows), g.col+1, len(g.result.Columns))
	}
	if g.result.Truncated {
		summary += fmt.Sprintf(" · TRUNCATED: %d rows retained", len(g.result.Rows))
	}
	return strings.Join(append(lines, dimStyle.Render(fit(summary, g.width))), "\n")
}
