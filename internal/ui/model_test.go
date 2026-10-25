package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"quick-query/internal/database"
	"quick-query/internal/profile"
)

func keypress(s string) tea.KeyPressMsg {
	parts := strings.Split(s, "+")
	var mod tea.KeyMod
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			mod |= tea.ModCtrl
		case "shift":
			mod |= tea.ModShift
		case "alt":
			mod |= tea.ModAlt
		}
	}
	k := parts[len(parts)-1]
	codes := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "backspace": tea.KeyBackspace, "f1": tea.KeyF1, "f2": tea.KeyF2, "f4": tea.KeyF4, "f5": tea.KeyF5, "f6": tea.KeyF6, "f7": tea.KeyF7, "f8": tea.KeyF8, "f9": tea.KeyF9, "f10": tea.KeyF10}
	code, ok := codes[k]
	text := ""
	if !ok {
		code = []rune(k)[0]
		if mod == 0 {
			text = k
		}
	}
	return tea.KeyPressMsg{Code: code, Mod: mod, Text: text}
}

type fakeSecrets struct{ values map[string]string }

func (s *fakeSecrets) Get(id string) (string, error) {
	v, ok := s.values[id]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}
func (s *fakeSecrets) Set(id, v string) error { s.values[id] = v; return nil }
func (s *fakeSecrets) Delete(id string) error { delete(s.values, id); return nil }

type fakeSession struct {
	sql    []string
	result database.Execution
	closed bool
}

func (s *fakeSession) Execute(_ context.Context, sql string) database.Execution {
	s.sql = append(s.sql, sql)
	return s.result
}
func (s *fakeSession) Close(context.Context) error { s.closed = true; return nil }

func newTestModel(t *testing.T) *Model {
	t.Helper()
	store := profile.FileStore{Path: filepath.Join(t.TempDir(), "connections.json")}
	manager := profile.Manager{Profiles: store, Secrets: &fakeSecrets{values: map[string]string{}}}
	m := New(manager, func(context.Context, profile.Profile, string) (database.Session, error) { return &fakeSession{}, nil })
	t.Cleanup(m.Close)
	return m
}

// Execute only a command batch's first (work) command, not the ticker.
func workMessage(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return batch[0]()
	}
	return msg
}

func TestEditorSelectionUndoRedoAndIndent(t *testing.T) {
	e := newEditor()
	e.input.SetWidth(50)
	e.input.SetHeight(8)
	e.input.Focus()
	e.input.SetValue("SELECT 1;\nSELECT '你好';")
	e.update(keypress("ctrl+home"))
	for i := 0; i < len("SELECT 1;"); i++ {
		e.update(keypress("shift+right"))
	}
	if got := e.sql(); got != "SELECT 1;" {
		t.Fatalf("selected SQL = %q", got)
	}
	e.update(keypress("x"))
	if !strings.HasPrefix(e.input.Value(), "x\n") {
		t.Fatalf("selection not replaced: %q", e.input.Value())
	}
	e.update(keypress("ctrl+z"))
	if e.input.Value() != "SELECT 1;\nSELECT '你好';" {
		t.Fatal("undo failed")
	}
	e.update(keypress("ctrl+y"))
	if !strings.HasPrefix(e.input.Value(), "x\n") {
		t.Fatal("redo failed")
	}
	e.input.SetValue("    SELECT 1;")
	e.update(keypress("enter"))
	if e.input.Value() != "    SELECT 1;\n    " {
		t.Fatalf("indent not preserved: %q", e.input.Value())
	}
	e.update(keypress("tab"))
	if !strings.HasSuffix(e.input.Value(), "\n        ") {
		t.Fatal("tab must insert spaces")
	}
	e.update(keypress("ctrl+a"))
	e.update(tea.PasteMsg{Content: "SELECT '🌍';\nSELECT 2;"})
	if e.input.Value() != "SELECT '🌍';\nSELECT 2;" {
		t.Fatal("bracketed paste failed")
	}
}

func TestExecutionFocusResultsAndFailure(t *testing.T) {
	m := newTestModel(t)
	s := &fakeSession{result: database.Execution{Results: []database.Result{{Columns: []string{"answer"}, Rows: [][]database.Cell{{{Text: "42"}}}, TotalRows: 1}, {Command: "CREATE TABLE"}}, TxStatus: 'I'}}
	m.session = s
	m.screen = workspaceScreen
	m.editor.input.Focus()
	m.editor.input.SetValue("SELECT 42;")
	_, cmd := m.Update(keypress("f5"))
	if !m.busy {
		t.Fatal("query must run asynchronously")
	}
	if _, duplicate := m.Update(keypress("f5")); duplicate != nil {
		t.Fatal("duplicate run was accepted")
	}
	m.Update(workMessage(t, cmd))
	if m.busy || !m.editorFocused || len(m.grids) != 2 || s.sql[0] != "SELECT 42;" {
		t.Fatal("execution state mismatch")
	}
	m.Update(keypress("f6"))
	if m.editorFocused || m.editor.input.Focused() {
		t.Fatal("editor still focused")
	}
	m.Update(keypress("enter"))
	if m.screen != cellScreen || m.viewerText != "42" {
		t.Fatal("cell viewer failed")
	}
	m.Update(keypress("f1"))
	m.Update(keypress("esc"))
	if m.screen != cellScreen || m.viewerText != "42" {
		t.Fatal("help lost cell viewer state")
	}
	m.Update(keypress("esc"))
	m.Update(keypress("f8"))
	if m.screen != workspaceScreen || m.resultIndex != 1 {
		t.Fatal("result tab navigation failed")
	}
	m.Update(keypress("f7"))
	m.Update(keypress("f6"))
	s.result = database.Execution{Err: errors.New("SQL failed"), Disconnected: true}
	_, cmd = m.Update(keypress("ctrl+r"))
	m.Update(workMessage(t, cmd))
	if m.session != nil || m.editor.input.Value() != "SELECT 42;" || !strings.Contains(m.lastError, "SQL failed") {
		t.Fatal("failure lost SQL or failed to disconnect")
	}
	if _, cmd = m.Update(keypress("f5")); cmd != nil {
		t.Fatal("disconnected query must not execute")
	}
}

func TestPromptedPasswordIsNotSavedOrReused(t *testing.T) {
	m := newTestModel(t)
	p := profile.New()
	p.Name = "Local"
	p.User = "test"
	p.Database = "postgres"
	profiles, _, err := m.manager.Save(p, "")
	if err != nil {
		t.Fatal(err)
	}
	m.profiles = profiles
	var supplied []string
	m.connect = func(_ context.Context, _ profile.Profile, password string) (database.Session, error) {
		supplied = append(supplied, password)
		return &fakeSession{}, nil
	}
	for i := 0; i < 2; i++ {
		m.screen = pickerScreen
		_, cmd := m.Update(keypress("enter"))
		m.Update(workMessage(t, cmd))
		if m.screen != passwordScreen || m.password.Value() != "" {
			t.Fatal("every connection must prompt")
		}
		m.password.SetValue(fmt.Sprintf("password-%d", i))
		_, cmd = m.Update(keypress("enter"))
		m.Update(workMessage(t, cmd))
		if m.screen != workspaceScreen || m.password.Value() != "" {
			t.Fatal("password retained in prompt")
		}
	}
	if strings.Join(supplied, ",") != "password-0,password-1" {
		t.Fatal("prompted password not used")
	}
	if len(m.manager.Secrets.(*fakeSecrets).values) != 0 {
		t.Fatal("prompted password persisted")
	}
}

func TestGridNavigationAndTerminalSafety(t *testing.T) {
	r := database.Result{Columns: []string{"id", "unicode", "null", "wide value"}, TotalRows: 100}
	for i := 0; i < 100; i++ {
		r.Rows = append(r.Rows, []database.Cell{{Text: fmt.Sprint(i)}, {Text: "你好 🌍"}, {Null: true}, {Text: strings.Repeat("wide", 30) + "\x1b[2J"}})
	}
	g := newGrid(r)
	g.size(40, 8)
	g.navigate("pgdown")
	g.navigate("end")
	if g.row == 0 || g.top == 0 || g.col != 3 || g.left == 0 {
		t.Fatal("grid did not scroll to selected cell")
	}
	for _, line := range strings.Split(g.view(true), "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("line too wide: %q", line)
		}
	}
	g.navigate("ctrl+end")
	if g.row != 99 {
		t.Fatal("last row navigation failed")
	}
	g.navigate("ctrl+home")
	if g.row != 0 || g.col != 0 || g.left != 0 {
		t.Fatal("first cell navigation failed")
	}
	if strings.Contains(safeText("\x1b[2J\x07", true), "\x1b") || strings.Contains(safeText("\x1b[2J\x07", true), "\x07") {
		t.Fatal("unsafe control characters rendered")
	}
	for _, result := range []database.Result{{}, {Columns: []string{"empty"}}, {Command: "UPDATE 0"}} {
		g := newGrid(result)
		g.size(30, 4)
		g.navigate("ctrl+end")
		g.view(true)
	}
}

func TestViewsFitTerminalAndResize(t *testing.T) {
	m := newTestModel(t)
	m.editor.input.SetValue(strings.Repeat("SELECT '你好 🌍';\n", 40))
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, screen := range []screen{pickerScreen, workspaceScreen, formScreen, passwordScreen, helpScreen} {
			if screen == formScreen {
				m.openForm(profile.New(), "", "")
			}
			if screen == helpScreen {
				m.setViewer("Help", helpText)
			}
			m.screen = screen
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != size[1] {
				t.Fatalf("screen %d at %v: %d lines", screen, size, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("screen %d at %v: width %d", screen, size, ansi.StringWidth(line))
				}
			}
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(m.View().Content, "Resize terminal") {
		t.Fatal("missing small terminal fallback")
	}
}

func TestCancelDoesNotQuitOrBlockNavigation(t *testing.T) {
	m := newTestModel(t)
	m.screen = workspaceScreen
	m.busy = true
	m.operation = "Executing"
	canceled := false
	m.cancel = func() { canceled = true }
	m.Update(keypress("f9"))
	if !canceled || m.quitting {
		t.Fatal("cancel must cancel query, not quit")
	}
	m.Update(keypress("f6"))
	if m.editorFocused {
		t.Fatal("busy query blocked focus change")
	}
	m.Update(keypress("f2"))
	if m.screen != workspaceScreen {
		t.Fatal("connection switch while busy was allowed")
	}
}
