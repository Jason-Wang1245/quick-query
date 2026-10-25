# Quick Query

A keyboard-driven PostgreSQL terminal client written in Go. Edit SQL in the top
panel and browse results in the bottom panel.

## Run

Requires Go 1.27.1 or newer to build and a terminal at least 60 columns × 20 rows.

```sh
go build -o quick-query .
./quick-query
```

Or install the command into your Go bin directory:

```sh
go install .
quick-query
```

## Connections

- Press **n** to create a connection. Enter a name, host, port, database,
  username, SSL mode, and optional password. Press **Ctrl+S** to save.
- Select a connection with **↑/↓**, then press **Enter** to connect.
- **e** edits a profile; **d** deletes it after confirmation; **F2** opens the
  connection picker from the workspace. **Esc** returns to the active session.
- Supplied passwords are stored in the OS keychain. Leave the password blank to
  prompt **each time you connect**. Clearing the password while editing removes
  its stored secret. Connection-time passwords are never saved.
- If the keychain is unavailable, the app explains the issue and uses a password
  prompt. Keychain access on Linux requires a running Secret Service provider
  such as GNOME Keyring; macOS uses Keychain and Windows uses Credential Manager.
- Profiles are saved with restricted permissions in
  `os.UserConfigDir()/quick-query/connections.json`. On macOS this is
  `~/Library/Application Support/quick-query/connections.json`; on Linux it is
  normally `~/.config/quick-query/connections.json`.
- Use `quick-query --config /path/to/connections.json` for an alternate profile
  file. Passwords still use the system keychain, identified by profile ID.
- Port defaults to **5432**, SSL mode to **prefer**. Supported SSL modes are
  `disable`, `allow`, `prefer`, `require`, `verify-ca`, and `verify-full`.
  Use `verify-full` for certificate and hostname verification; pgx recognizes
  PostgreSQL certificate environment variables such as `PGSSLROOTCERT`.
  Hosts can also be Unix socket directories.

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| Ctrl+R / F5 | Run selection, or the entire editor if nothing is selected |
| F6 | Switch editor/results focus |
| F2 | Manage connections |
| F4 | Show full query error |
| F7 / F8 | Previous/next result tab |
| F9 | Cancel a query or connection attempt |
| F1 | Show help |
| F10 / Ctrl+Q | Quit |
| Arrows | Move editor cursor or selected result cell |
| Page Up / Page Down | Page the focused panel |
| Home / End | Line start/end in editor; first/last column in results |
| Ctrl+Home / Ctrl+End | Start/end of editor or results |
| Shift+Arrows | Select SQL |
| Ctrl+A | Select all SQL |
| Ctrl+C / Ctrl+X / Ctrl+V | Copy/cut/paste SQL |
| Ctrl+Z / Ctrl+Y | Undo/redo |
| Tab | Insert four spaces in editor; advance connection form field |
| Enter in results | Open expanded cell viewer |
| Esc | Close a dialog/viewer |

Terminal paste, such as **Cmd+V** on macOS, is supported. Function keys may require
**Fn** on macOS. Ctrl+C copies editor selections; use F9 to cancel a query and
F10 to quit. Clipboard shortcuts require the OS clipboard to be available.

## Query behavior

- SQL executes exactly as entered, including writes and transaction commands.
  One active session preserves transactions, temporary tables, and session settings.
- Multiple statements produce separate result tabs and follow PostgreSQL's
  simple-query batch transaction semantics. An error can roll back earlier
  statements in that batch. Command tags alone do not prove a transaction committed.
- Each run replaces the previous results. The UI remains responsive while a query
  executes; F9 requests cancellation. Lost connections require reconnecting with
  F2, and queries are never automatically replayed.
- Retention is capped at **1,000 rows per result** and **16 MiB per execution**
  (including an allowance for metadata). Truncated results are labeled. Additional
  server output is drained so later statements still execute; this does not add
  a SQL `LIMIT` or reduce database work. An individual oversized row may result
  in no retained rows. Driver buffers are additional transient memory.
- `NULL` is displayed explicitly; empty strings appear blank. Use Enter to inspect
  a cell's full retained contents. Control characters in database values are
  rendered visibly rather than interpreted by the terminal.
- Query text/results are session-only. Autocomplete, syntax highlighting, schema
  browsing, SQL file management, persistent history, and other database engines
  are outside this version. PostgreSQL interactive `COPY FROM STDIN` / `COPY TO
  STDOUT` workflows are not provided.

## Development and tests

```sh
go test -race ./...
go vet ./...
```

Database integration tests run when `QUICK_QUERY_TEST_PORT` is set. Point them at
a **disposable local PostgreSQL instance** with trust authentication, a superuser
named `quickquery`, and database `postgres`. Tests use temporary tables and include
terminating their own database connection.

```sh
QUICK_QUERY_TEST_PORT=55439 go test -race ./...
```

The packages separate connection profiles/keychain access, PostgreSQL execution,
and the TUI. UI tests drive keyboard events directly; no live database or OS
keychain is needed for ordinary unit tests.
