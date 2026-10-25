package database

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"quick-query/internal/profile"
)

const MaxRows = 1000
const MaxBytes = 16 * 1024 * 1024

type Cell struct {
	Text string
	Null bool
}

type Result struct {
	Columns   []string
	Rows      [][]Cell
	TotalRows int64
	Command   string
	Truncated bool
}

type Execution struct {
	Results      []Result
	Duration     time.Duration
	Err          error
	Disconnected bool
	TxStatus     byte
	Truncated    bool
}

type Session interface {
	Execute(context.Context, string) Execution
	Close(context.Context) error
}

type Connector func(context.Context, profile.Profile, string) (Session, error)

type Postgres struct {
	mu   sync.Mutex
	conn *pgconn.PgConn
}

func Connect(ctx context.Context, p profile.Profile, password string) (Session, error) {
	u := &url.URL{Scheme: "postgresql", Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), User: url.User(p.User), Path: "/" + p.Database}
	q := url.Values{"sslmode": {p.SSLMode}, "connect_timeout": {"10"}, "application_name": {"quick-query"}}
	// PostgreSQL also accepts a Unix socket directory as the host.
	if strings.HasPrefix(p.Host, "/") {
		u.Host = ""
		q.Set("host", p.Host)
		q.Set("port", strconv.Itoa(p.Port))
	}
	u.RawQuery = q.Encode()
	cfg, err := pgconn.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection settings")
	}
	// Set after parsing so .pgpass/PGPASSWORD cannot override prompt mode.
	cfg.Password = password
	cfg.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: conn, DeadlineDelay: 3 * time.Second}
	}
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Postgres{conn: conn}, nil
}

func (p *Postgres) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn.Close(ctx)
}

func (p *Postgres) Execute(ctx context.Context, sql string) (execution Execution) {
	p.mu.Lock()
	defer p.mu.Unlock()
	start := time.Now()
	defer func() {
		execution.Duration = time.Since(start)
		execution.Disconnected = p.conn.IsClosed()
		if !execution.Disconnected {
			execution.TxStatus = p.conn.TxStatus()
		}
		if ctx.Err() != nil {
			execution.Err = ctx.Err()
		}
	}()
	remaining := MaxBytes
	reader := p.conn.Exec(ctx, sql)
	for reader.NextResult() {
		rr := reader.ResultReader()
		result := Result{}
		// Account for result/column/row overhead too, including empty and NULL cells.
		retain := remaining >= 128
		if retain {
			remaining -= 128
		}
		for _, f := range rr.FieldDescriptions() {
			cost := len(f.Name) + 16
			if remaining < cost {
				retain = false
				break
			}
			remaining -= cost
			result.Columns = append(result.Columns, strings.Clone(f.Name))
		}
		rowsFull := false
		for rr.NextRow() {
			result.TotalRows++
			if !retain || rowsFull || len(result.Rows) >= MaxRows {
				result.Truncated = true
				continue
			}
			values := rr.Values()
			cost := 24 + len(values)*32
			for _, v := range values {
				cost += len(v)
			}
			if cost > remaining {
				result.Truncated = true
				rowsFull = true
				continue
			}
			remaining -= cost
			row := make([]Cell, len(values))
			for i, value := range values {
				row[i] = Cell{Text: string(value), Null: value == nil}
			}
			result.Rows = append(result.Rows, row)
		}
		tag, err := rr.Close()
		if len(tag.String()) <= remaining {
			result.Command = tag.String()
			remaining -= len(result.Command)
		} else {
			result.Truncated = true
		}
		if err != nil {
			execution.Err = err
		}
		if retain {
			execution.Results = append(execution.Results, result)
		} else {
			execution.Truncated = true
		}
		if result.Truncated {
			execution.Truncated = true
		}
	}
	if err := reader.Close(); err != nil {
		execution.Err = err
	}
	return execution
}

func ErrorText(err error) string {
	if errors.Is(err, context.Canceled) {
		return "Query canceled."
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		s := fmt.Sprintf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
		if pgErr.Position > 0 {
			s += fmt.Sprintf(" · character %d", pgErr.Position)
		}
		if pgErr.Detail != "" {
			s += "\n" + pgErr.Detail
		}
		if pgErr.Hint != "" {
			s += "\nHint: " + pgErr.Hint
		}
		return s
	}
	return err.Error()
}
