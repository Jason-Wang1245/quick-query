package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

type snapshot struct {
	text     string
	row, col int
}

type editor struct {
	input      textarea.Model
	undo, redo []snapshot
}

func newEditor() editor {
	t := textarea.New()
	t.Placeholder = "SELECT * FROM your_table LIMIT 100;"
	t.Prompt = ""
	t.ShowLineNumbers = true
	// Bubbles derives the line-number gutter from MaxHeight even when only a
	// few rows are visible. Reserve all five digits so long buffers don't clip.
	t.MaxHeight = 10000
	t.MaxWidth = 0
	t.CharLimit = 1024 * 1024
	t.SetVirtualCursor(true)
	t.KeyMap.SelectAll = key.NewBinding(key.WithKeys("ctrl+a"))
	t.KeyMap.LineStart = key.NewBinding(key.WithKeys("home"))
	t.KeyMap.CopySelection = key.NewBinding(key.WithKeys("ctrl+c", "ctrl+shift+c"))
	return editor{input: t}
}

func (e *editor) snapshot() snapshot {
	return snapshot{e.input.Value(), e.input.Line(), e.input.Column()}
}

func (e *editor) restore(s snapshot) {
	e.input.SetValue(s.text)
	for e.input.Line() > s.row {
		e.input.CursorUp()
	}
	e.input.SetCursorColumn(s.col)
	e.input.ClearSelection()
}

func pushSnapshot(stack []snapshot, s snapshot) []snapshot {
	stack = append(stack, s)
	if len(stack) > 200 {
		stack = append([]snapshot(nil), stack[len(stack)-200:]...)
	}
	return stack
}

func (e *editor) sql() string {
	if e.input.HasSelection() {
		return e.input.SelectedText()
	}
	return e.input.Value()
}

func (e *editor) update(msg tea.Msg) tea.Cmd {
	before := e.snapshot()
	var cmd tea.Cmd
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "ctrl+z":
			if len(e.undo) > 0 {
				e.redo = pushSnapshot(e.redo, before)
				e.restore(e.undo[len(e.undo)-1])
				e.undo = e.undo[:len(e.undo)-1]
			}
			return nil
		case "ctrl+y", "ctrl+shift+z":
			if len(e.redo) > 0 {
				e.undo = pushSnapshot(e.undo, before)
				e.restore(e.redo[len(e.redo)-1])
				e.redo = e.redo[:len(e.redo)-1]
			}
			return nil
		case "ctrl+x":
			cmd = e.input.CopySelection()
			e.input.DeleteSelection()
		case "tab":
			e.input.DeleteSelection()
			e.input.InsertString("    ")
		case "enter":
			lines := strings.Split(e.input.Value(), "\n")
			line := lines[e.input.Line()]
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			e.input.DeleteSelection()
			e.input.InsertString("\n" + indent)
		default:
			e.input, cmd = e.input.Update(msg)
		}
	} else {
		e.input, cmd = e.input.Update(msg)
	}
	if before.text != e.input.Value() {
		e.undo = pushSnapshot(e.undo, before)
		e.redo = nil
	}
	return cmd
}
