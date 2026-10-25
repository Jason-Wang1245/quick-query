package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"quick-query/internal/database"
	"quick-query/internal/profile"
)

type screen uint8

const (
	pickerScreen screen = iota
	workspaceScreen
	formScreen
	passwordScreen
	deleteScreen
	helpScreen
	cellScreen
)

type profilesMsg struct {
	profiles []profile.Profile
	err      error
}
type savedMsg struct {
	profiles []profile.Profile
	warning  string
	err      error
}
type editMsg struct {
	p                 profile.Profile
	password, warning string
}
type passwordMsg struct {
	p                     profile.Profile
	password, explanation string
	prompt                bool
}
type connectedMsg struct {
	p       profile.Profile
	session database.Session
	err     error
}
type executedMsg struct{ execution database.Execution }
type tickMsg struct{}

type Model struct {
	manager                       profile.Manager
	connect                       database.Connector
	ctx                           context.Context
	stop                          context.CancelFunc
	cancel                        context.CancelFunc
	session                       database.Session
	active                        profile.Profile
	profiles                      []profile.Profile
	selected                      int
	screen, previous              screen
	helpReturn                    screen
	helpSavedText, helpSavedTitle string
	helpOffset                    int
	editor                        editor
	editorFocused                 bool
	grids                         []grid
	resultIndex                   int
	fields                        []textinput.Model
	fieldIndex                    int
	formProfile                   profile.Profile
	password                      textinput.Model
	pending                       profile.Profile
	viewer                        viewport.Model
	viewerText, viewerTitle       string
	width, height                 int
	busy, quitting                bool
	operation                     string
	started                       time.Time
	status, notice                string
	lastError                     string
	loadFailed                    bool
	txStatus                      byte
	lastDuration                  time.Duration
}

func New(manager profile.Manager, connect database.Connector) *Model {
	ctx, stop := context.WithCancel(context.Background())
	m := &Model{manager: manager, connect: connect, ctx: ctx, stop: stop, editor: newEditor(), editorFocused: true, width: 80, height: 24, status: "Choose a connection", screen: pickerScreen}
	m.password = textinput.New()
	m.password.Prompt = ""
	m.password.EchoMode = textinput.EchoPassword
	m.password.EchoCharacter = '•'
	m.password.SetVirtualCursor(true)
	m.viewer = viewport.New()
	m.layout()
	return m
}

func (m *Model) Init() tea.Cmd {
	manager := m.manager
	return func() tea.Msg { profiles, err := manager.Profiles.Load(); return profilesMsg{profiles, err} }
}

// Close is called after the UI stops, including after terminal/signal errors.
func (m *Model) Close() {
	m.stop()
	if m.cancel != nil {
		m.cancel()
	}
	if m.session != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		_ = m.session.Close(ctx)
	}
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) finishOperation() {
	m.busy = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil
	case profilesMsg:
		m.profiles = msg.profiles
		if msg.err != nil {
			m.loadFailed = true
			m.notice = msg.err.Error()
		}
		return m, nil
	case savedMsg:
		m.finishOperation()
		if m.quitting {
			return m, tea.Quit
		}
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		m.profiles = msg.profiles
		m.selected = min(m.selected, max(0, len(m.profiles)-1))
		m.fields = nil // release passwords held by the form
		m.screen = pickerScreen
		m.notice = msg.warning
		m.status = "Connections updated"
		return m, nil
	case editMsg:
		m.finishOperation()
		if m.quitting {
			return m, tea.Quit
		}
		return m, m.openForm(msg.p, msg.password, msg.warning)
	case passwordMsg:
		m.finishOperation()
		if m.quitting {
			return m, tea.Quit
		}
		m.pending = msg.p
		if msg.prompt {
			m.screen = passwordScreen
			m.notice = msg.explanation
			m.password.SetValue("")
			return m, m.password.Focus()
		}
		return m, m.startConnect(msg.p, msg.password)
	case connectedMsg:
		m.finishOperation()
		m.password.SetValue("")
		m.password.Blur()
		if msg.err != nil {
			m.screen = pickerScreen
			m.notice = "Connection failed: " + database.ErrorText(msg.err)
			if m.quitting {
				return m, tea.Quit
			}
			return m, nil
		}
		m.session = msg.session
		m.active = msg.p
		m.grids = nil
		m.resultIndex = 0
		m.txStatus = 'I'
		m.lastError = ""
		m.notice = ""
		m.status = "Connected"
		m.lastDuration = 0
		if m.quitting {
			return m, tea.Quit
		}
		m.screen = workspaceScreen
		m.editorFocused = true
		return m, m.editor.input.Focus()
	case executedMsg:
		m.finishOperation()
		m.grids = nil
		m.resultIndex = 0
		m.lastDuration = msg.execution.Duration
		m.txStatus = msg.execution.TxStatus
		for _, result := range msg.execution.Results {
			m.grids = append(m.grids, newGrid(result))
		}
		m.status = fmt.Sprintf("Completed · %d result(s)", len(m.grids))
		m.lastError = ""
		if msg.execution.Truncated {
			m.status += " · results truncated"
		}
		if msg.execution.Err != nil {
			m.status = "Execution failed · earlier results may have rolled back"
			m.lastError = database.ErrorText(msg.execution.Err)
		}
		if msg.execution.Disconnected {
			m.session = nil
			m.status = "Disconnected · reconnect with F2"
		}
		m.layout()
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil
	case tickMsg:
		if m.busy {
			return m, tick()
		}
		return m, nil
	case tea.KeyPressMsg:
		k := msg.String()
		if k == "f10" || k == "ctrl+q" || (k == "ctrl+c" && m.screen == pickerScreen) {
			if m.busy {
				m.quitting = true
				if m.cancel != nil {
					m.cancel()
				}
				m.status = "Closing…"
				return m, nil
			}
			return m, tea.Quit
		}
		if k == "f9" {
			if m.cancel != nil {
				m.cancel()
				m.status = "Canceling…"
			}
			return m, nil
		}
		if k == "f1" {
			if m.screen == helpScreen {
				m.closeHelp()
				return m, nil
			}
			m.helpReturn = m.screen
			m.helpSavedText, m.helpSavedTitle = m.viewerText, m.viewerTitle
			m.helpOffset = m.viewer.YOffset()
			m.screen = helpScreen
			m.setViewer("Keyboard shortcuts", helpText)
			return m, nil
		}
		// Keep result browsing and SQL editing usable during execution. Other
		// operations serialize form/profile/connection state changes.
		if m.busy && m.operation != "Executing" && m.screen != helpScreen {
			return m, nil
		}
		if m.screen == helpScreen || m.screen == cellScreen {
			if k == "esc" {
				if m.screen == helpScreen {
					m.closeHelp()
				} else {
					m.screen = m.previous
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.viewer, cmd = m.viewer.Update(msg)
			return m, cmd
		}
		switch m.screen {
		case pickerScreen:
			return m, m.updatePicker(k)
		case formScreen:
			return m, m.updateForm(msg)
		case passwordScreen:
			if k == "esc" {
				m.password.SetValue("")
				m.password.Blur()
				m.screen = pickerScreen
				m.notice = ""
				return m, nil
			}
			if k == "enter" {
				password := m.password.Value()
				m.password.SetValue("")
				return m, m.startConnect(m.pending, password)
			}
		case deleteScreen:
			if k == "esc" || k == "n" {
				m.screen = pickerScreen
				return m, nil
			}
			if k == "y" || k == "enter" {
				id := m.profiles[m.selected].ID
				manager := m.manager
				m.busy = true
				m.operation = "Deleting"
				m.started = time.Now()
				return m, tea.Batch(func() tea.Msg { p, w, e := manager.Delete(id); return savedMsg{p, w, e} }, tick())
			}
			return m, nil
		case workspaceScreen:
			switch k {
			case "f4":
				if m.lastError != "" {
					m.previous = workspaceScreen
					m.screen = cellScreen
					m.setViewer("Execution error", m.lastError)
				}
				return m, nil
			case "f2":
				if m.busy {
					m.status = "Cancel or finish the query before switching connections"
					return m, nil
				}
				m.screen = pickerScreen
				m.notice = ""
				m.editor.input.Blur()
				return m, nil
			case "f6":
				m.editorFocused = !m.editorFocused
				if m.editorFocused {
					return m, m.editor.input.Focus()
				}
				m.editor.input.Blur()
				return m, nil
			case "ctrl+r", "f5":
				return m, m.runSQL()
			case "f7", "f8":
				if len(m.grids) > 0 {
					if k == "f7" {
						m.resultIndex = (m.resultIndex + len(m.grids) - 1) % len(m.grids)
					} else {
						m.resultIndex = (m.resultIndex + 1) % len(m.grids)
					}
				}
				return m, nil
			}
			if !m.editorFocused {
				if len(m.grids) > 0 {
					g := &m.grids[m.resultIndex]
					if k == "enter" {
						if cell, ok := g.cell(); ok {
							text := cell.Text
							if cell.Null {
								text = "NULL (no value)"
							} else if text == "" {
								text = "(empty string)"
							}
							m.previous = workspaceScreen
							m.screen = cellScreen
							m.setViewer(fmt.Sprintf("Row %d · %s", g.row+1, g.result.Columns[g.col]), text)
						}
					} else {
						g.navigate(k)
					}
				}
				return m, nil
			}
		}
	}
	if m.screen == workspaceScreen && m.editorFocused {
		return m, m.editor.update(msg)
	}
	if m.screen == passwordScreen {
		var cmd tea.Cmd
		m.password, cmd = m.password.Update(msg)
		return m, cmd
	}
	if m.screen == formScreen && len(m.fields) > 0 {
		var cmd tea.Cmd
		m.fields[m.fieldIndex], cmd = m.fields[m.fieldIndex].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) updatePicker(k string) tea.Cmd {
	switch k {
	case "up":
		m.selected = max(0, m.selected-1)
	case "down":
		m.selected = min(max(0, len(m.profiles)-1), m.selected+1)
	case "pgup":
		m.selected = max(0, m.selected-10)
	case "pgdown":
		m.selected = min(max(0, len(m.profiles)-1), m.selected+10)
	case "esc", "f2":
		if m.session != nil {
			m.screen = workspaceScreen
			if m.editorFocused {
				return m.editor.input.Focus()
			}
		}
	case "n":
		if !m.loadFailed {
			return m.openForm(profile.New(), "", "")
		}
	case "e":
		if len(m.profiles) == 0 || m.loadFailed {
			return nil
		}
		p := m.profiles[m.selected]
		secrets := m.manager.Secrets
		m.busy = true
		m.operation = "Loading profile"
		m.started = time.Now()
		return tea.Batch(func() tea.Msg {
			password, warning := "", ""
			if p.PasswordMode == profile.Keychain {
				var err error
				password, err = secrets.Get(p.ID)
				if err != nil {
					password = ""
					warning = "Saved password unavailable. Saving a blank password will enable prompts."
				}
			}
			return editMsg{p, password, warning}
		}, tick())
	case "d", "delete":
		if len(m.profiles) > 0 && !m.loadFailed {
			m.screen = deleteScreen
			m.notice = ""
		}
	case "enter":
		if len(m.profiles) == 0 {
			if !m.loadFailed {
				return m.openForm(profile.New(), "", "")
			}
			return nil
		}
		p := m.profiles[m.selected]
		manager := m.manager
		m.busy = true
		m.operation = "Reading credentials"
		m.started = time.Now()
		m.notice = ""
		return tea.Batch(func() tea.Msg {
			password, explanation, prompt := manager.Password(p)
			return passwordMsg{p, password, explanation, prompt}
		}, tick())
	}
	return nil
}

func (m *Model) startConnect(p profile.Profile, password string) tea.Cmd {
	ctx, cancel := context.WithTimeout(m.ctx, 12*time.Second)
	m.cancel = cancel
	m.busy = true
	m.operation = "Connecting"
	m.status = "Connecting…"
	m.started = time.Now()
	connect, previous := m.connect, m.session
	return tea.Batch(func() tea.Msg {
		session, err := connect(ctx, p, password)
		if err == nil && previous != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = previous.Close(closeCtx)
			cancel()
		}
		return connectedMsg{p, session, err}
	}, tick())
}

func (m *Model) runSQL() tea.Cmd {
	if m.busy {
		return nil
	}
	if m.session == nil {
		m.status = "Reconnect with F2 before executing SQL"
		return nil
	}
	sql := m.editor.sql()
	if strings.TrimSpace(sql) == "" {
		m.status = "Enter or select SQL to execute"
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.busy = true
	m.operation = "Executing"
	m.started = time.Now()
	m.status = "Executing…"
	m.grids = nil
	m.lastError = ""
	m.resultIndex = 0
	m.lastDuration = 0
	session := m.session
	return tea.Batch(func() tea.Msg { return executedMsg{session.Execute(ctx, sql)} }, tick())
}

var fieldNames = []string{"Name", "Host", "Port", "Database", "Username", "SSL mode", "Password"}

func (m *Model) openForm(p profile.Profile, password, warning string) tea.Cmd {
	m.formProfile = p
	m.screen = formScreen
	m.fieldIndex = 0
	m.notice = warning
	values := []string{p.Name, p.Host, strconv.Itoa(p.Port), p.Database, p.User, p.SSLMode, password}
	m.fields = make([]textinput.Model, len(values))
	for i, value := range values {
		f := textinput.New()
		f.Prompt = ""
		f.SetVirtualCursor(true)
		f.SetWidth(max(10, m.width-22))
		f.SetValue(value)
		if i == 6 {
			f.EchoMode = textinput.EchoPassword
			f.EchoCharacter = '•'
			f.Placeholder = "blank = prompt each time"
		}
		m.fields[i] = f
	}
	return m.fields[0].Focus()
}

func (m *Model) updateForm(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.fields = nil
		m.screen = pickerScreen
		m.notice = ""
		return nil
	case "ctrl+s":
		return m.saveForm()
	case "tab", "down", "enter", "shift+tab", "up":
		if msg.String() == "enter" && m.fieldIndex == len(m.fields)-1 {
			return m.saveForm()
		}
		m.fields[m.fieldIndex].Blur()
		if msg.String() == "shift+tab" || msg.String() == "up" {
			m.fieldIndex = (m.fieldIndex + len(m.fields) - 1) % len(m.fields)
		} else {
			m.fieldIndex = (m.fieldIndex + 1) % len(m.fields)
		}
		return m.fields[m.fieldIndex].Focus()
	default:
		var cmd tea.Cmd
		m.fields[m.fieldIndex], cmd = m.fields[m.fieldIndex].Update(msg)
		return cmd
	}
}

func (m *Model) saveForm() tea.Cmd {
	p := m.formProfile
	p.Name = strings.TrimSpace(m.fields[0].Value())
	p.Host = strings.TrimSpace(m.fields[1].Value())
	port, err := strconv.Atoi(m.fields[2].Value())
	if err != nil {
		m.notice = "Port must be a number between 1 and 65535"
		return nil
	}
	p.Port = port
	p.Database = strings.TrimSpace(m.fields[3].Value())
	p.User = strings.TrimSpace(m.fields[4].Value())
	p.SSLMode = strings.TrimSpace(m.fields[5].Value())
	if err := p.Validate(); err != nil {
		m.notice = err.Error()
		return nil
	}
	password := m.fields[6].Value()
	manager := m.manager
	m.busy = true
	m.operation = "Saving"
	m.started = time.Now()
	m.notice = ""
	return tea.Batch(func() tea.Msg {
		profiles, warning, err := manager.Save(p, password)
		return savedMsg{profiles, warning, err}
	}, tick())
}

func (m *Model) panelHeights() (int, int) {
	available := max(12, m.height-2)
	editorHeight := max(6, available*40/100)
	return editorHeight, available - editorHeight
}

func (m *Model) layout() {
	eh, rh := m.panelHeights()
	m.editor.input.SetWidth(max(10, m.width-2))
	m.editor.input.SetHeight(max(1, eh-3))
	for i := range m.grids {
		m.grids[i].size(max(1, m.width-2), max(1, rh-4))
	}
	for i := range m.fields {
		m.fields[i].SetWidth(max(10, m.width-22))
	}
	m.password.SetWidth(max(10, m.width-8))
	m.viewer.SetWidth(max(10, m.width-6))
	m.viewer.SetHeight(max(1, m.height-8))
	if m.viewerText != "" {
		m.viewer.SetContent(ansi.Hardwrap(m.viewerText, max(10, m.width-6), true))
	}
}

func (m *Model) setViewer(title, text string) {
	m.viewerTitle = safeText(title, false)
	m.viewerText = safeText(text, true)
	m.layout()
	m.viewer.GotoTop()
}

func (m *Model) closeHelp() {
	m.screen = m.helpReturn
	m.setViewer(m.helpSavedTitle, m.helpSavedText)
	m.viewer.SetYOffset(m.helpOffset)
	if m.screen == workspaceScreen && m.editorFocused {
		m.editor.input.Focus()
	}
}

func panel(content string, width, height int, focused bool) string {
	color := muted
	if focused {
		color = accent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Width(max(1, width)).Height(max(1, height)).MaxHeight(height).Render(content)
}

func (m *Model) View() tea.View {
	if m.width < 60 || m.height < 20 {
		v := tea.NewView(fmt.Sprintf("Quick Query\nResize terminal to at least 60×20 (currently %d×%d).\nF9 cancel · F10 quit", m.width, m.height))
		v.AltScreen = true
		return v
	}
	connection := "No connection"
	if m.session != nil {
		connection = m.active.Name + " · " + m.active.User + "@" + m.active.Host + "/" + m.active.Database
	}
	if m.txStatus == 'T' {
		connection += " · IN TRANSACTION"
	} else if m.txStatus == 'E' {
		connection += " · TRANSACTION FAILED"
	}
	top := headerStyle.Render(fit(" QUICK QUERY  "+safeText(connection, false), m.width))
	var body, footer string
	if m.screen == workspaceScreen {
		eh, rh := m.panelHeights()
		editorTitle := " SQL EDITOR"
		if m.editorFocused {
			editorTitle += " · active"
		}
		if m.editor.input.HasSelection() {
			editorTitle += " · selection will execute"
		}
		topPanel := panel(headerStyle.Render(editorTitle)+"\n"+m.editor.input.View(), m.width, eh, m.editorFocused)
		resultTitle := " RESULTS"
		if !m.editorFocused {
			resultTitle += " · active"
		}
		if len(m.grids) > 0 {
			resultTitle += fmt.Sprintf(" · %d/%d · F7/F8 switch", m.resultIndex+1, len(m.grids))
		}
		resultBody := dimStyle.Render("\n  Run SQL with Ctrl+R or F5.")
		if m.busy {
			resultBody = dimStyle.Render("\n  Running query… F9 cancels. You can keep editing.")
		}
		if len(m.grids) > 0 {
			resultBody = m.grids[m.resultIndex].view(!m.editorFocused)
		}
		if m.lastError != "" {
			resultTitle += " · F4 error details"
			resultBody = errorStyle.Render(fit(" "+safeText(m.lastError, false), m.width-2)) + "\n" + resultBody
		}
		body = topPanel + "\n" + panel(headerStyle.Render(resultTitle)+"\n"+resultBody, m.width, rh, !m.editorFocused)
		footer = " F5 run · F6 focus · F2 connections · F9 cancel · F1 help · F10 quit"
	} else {
		body = panel(m.screenContent(), m.width, m.height-2, true)
		switch m.screen {
		case pickerScreen:
			footer = " ↑↓ select · Enter connect · n new · e edit · d delete · Esc back · F10 quit"
		case formScreen:
			footer = " Tab/↑↓ move · Ctrl+S save · Esc cancel · F1 help"
		case passwordScreen:
			footer = " Enter connect · Esc cancel · F9 cancel connection · F10 quit"
		case deleteScreen:
			footer = " Enter/y delete · Esc/n cancel"
		default:
			footer = " ↑↓/PgUp/PgDn scroll · Esc back · F1 help · F10 quit"
		}
	}
	status := m.status
	if m.busy {
		status = fmt.Sprintf("%s · %.1fs", m.operation, time.Since(m.started).Seconds())
	} else if m.lastDuration > 0 {
		status += fmt.Sprintf(" · %.3fs", m.lastDuration.Seconds())
	}
	// Status lives in the top bar so shortcuts always remain visible.
	top = headerStyle.Render(fit(" QUICK QUERY  "+safeText(connection, false), max(20, m.width*55/100))) + dimStyle.Render(fit(safeText(status, false), m.width-max(20, m.width*55/100)))
	v := tea.NewView(top + "\n" + body + "\n" + dimStyle.Render(fit(footer, m.width)))
	v.AltScreen = true
	return v
}

func (m *Model) screenContent() string {
	w := m.width - 6
	var content string
	switch m.screen {
	case pickerScreen:
		content = "\n  " + headerStyle.Render("CONNECTIONS") + "\n  Choose a saved PostgreSQL connection.\n\n"
		if len(m.profiles) == 0 {
			content += "  No saved connections. Press n to create one.\n"
		}
		visible := max(1, m.height-12)
		start := max(0, m.selected-visible+1)
		for i := start; i < len(m.profiles) && i < start+visible; i++ {
			p := m.profiles[i]
			mode := "ask for password"
			if p.PasswordMode == profile.Keychain {
				mode = "keychain"
			}
			line := fmt.Sprintf("  %-18s  %s@%s:%d/%s · %s", safeText(p.Name, false), safeText(p.User, false), safeText(p.Host, false), p.Port, safeText(p.Database, false), mode)
			if i == m.selected {
				content += selectedStyle.Render(fit("›"+strings.TrimPrefix(line, " "), m.width-2)) + "\n"
			} else {
				content += fit(line, m.width-2) + "\n"
			}
		}
	case formScreen:
		content = "\n  " + headerStyle.Render("SAVE CONNECTION") + "\n  A blank password prompts each time you connect.\n\n"
		for i, f := range m.fields {
			label := fmt.Sprintf("  %-11s", fieldNames[i])
			if i == m.fieldIndex {
				label = headerStyle.Render(label)
			}
			content += label + f.View() + "\n"
		}
		content += "\n  SSL: disable / allow / prefer / require / verify-ca / verify-full\n"
	case passwordScreen:
		content = "\n  " + headerStyle.Render("CONNECT TO "+safeText(m.pending.Name, false)) + "\n\n  Password\n  " + m.password.View() + "\n"
	case deleteScreen:
		content = "\n  " + headerStyle.Render("DELETE CONNECTION") + "\n\n  Delete " + safeText(m.profiles[m.selected].Name, false) + " and its saved password?\n  This removes the saved profile. The database is unaffected.\n"
	case helpScreen, cellScreen:
		content = " " + headerStyle.Render(fit(m.viewerTitle, w)) + "\n\n " + strings.ReplaceAll(m.viewer.View(), "\n", "\n ")
	}
	if m.notice != "" && m.screen != helpScreen && m.screen != cellScreen {
		content += "\n  " + errorStyle.Render(ansi.Hardwrap(safeText(m.notice, true), w, true))
	}
	return content
}

const helpText = `WORKSPACE
Ctrl+R / F5    Execute selected SQL, otherwise the whole editor
F6            Switch editor / results focus
F2            Manage connections (after query completes)
F4            Show full execution error
F7 / F8       Previous / next result tab
F9            Cancel execution or connection
F10 / Ctrl+Q  Quit

EDITOR
Arrows, Home, End, Ctrl+Home/End, Page Up/Down: navigate
Shift+Arrows: select text; Ctrl+A: select all
Ctrl+C / Ctrl+X / Ctrl+V: copy / cut / paste
Ctrl+Z / Ctrl+Y: undo / redo
Tab: insert four spaces; Enter: newline with indentation
Terminal paste (Cmd+V on macOS) is also supported.

RESULTS
Arrows: move between cells; Page Up/Down: page rows
Home/End: first/last column; Ctrl+Home/End: first/last cell
Enter: expanded cell viewer; Esc: close viewer
NULL means SQL NULL; an empty cell is an empty string.

EXECUTION
SQL runs as entered, including writes and transaction commands.
Multiple statements follow PostgreSQL's batch transaction rules.
An error can roll back earlier statements in the same batch.
At most 1,000 rows/result and 16 MiB/run are retained. Further
output is drained without retaining it; F9 cancels long queries.
Lost sessions require reconnection; SQL is never replayed.

CONNECTIONS
n: new; e: edit; d: delete; Enter: connect; Esc: workspace
Passwords supplied when saving go to the system keychain.
Clear the password when editing to prompt on every connection.
Passwords entered at connection time are never saved.
SSL defaults to prefer; use verify-full for verified TLS.
On macOS, function keys may require holding Fn.`
