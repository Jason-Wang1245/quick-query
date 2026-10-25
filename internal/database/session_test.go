package database

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"quick-query/internal/profile"
)

// Integration tests are opt-in and should target a disposable PostgreSQL
// instance. Every object created below is session-local (a temporary table).
func testSession(t *testing.T) Session {
	t.Helper()
	portText := os.Getenv("QUICK_QUERY_TEST_PORT")
	if portText == "" {
		t.Skip("set QUICK_QUERY_TEST_PORT to test against a disposable PostgreSQL instance")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	p := profile.New()
	p.Name = "Integration"
	p.Port = port
	p.User = "quickquery"
	p.Database = "postgres"
	p.SSLMode = "disable"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Connect(ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s
}

func exec(t *testing.T, s Session, sql string) Execution {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := s.Execute(ctx, sql)
	if r.Err != nil {
		t.Fatalf("execute: %v", r.Err)
	}
	return r
}

func TestMultipleResultsAndCellValues(t *testing.T) {
	s := testSession(t)
	r := exec(t, s, "SELECT NULL::text AS missing, ''::text AS empty, '你好 🌍'::text AS unicode; SELECT 2 AS next;")
	if len(r.Results) != 2 || !r.Results[0].Rows[0][0].Null || r.Results[0].Rows[0][1].Null || r.Results[0].Rows[0][1].Text != "" || r.Results[1].Rows[0][0].Text != "2" {
		t.Fatalf("unexpected results: %+v", r)
	}
	if r.Results[0].Rows[0][2].Text != "你好 🌍" {
		t.Fatal("Unicode not preserved")
	}
	r = exec(t, s, "SELECT 1 AS empty WHERE false")
	if len(r.Results[0].Rows) != 0 || len(r.Results[0].Columns) != 1 {
		t.Fatal("empty result lost its header")
	}
}

func TestSessionTransactionsAndBatchRollback(t *testing.T) {
	s := testSession(t)
	r := exec(t, s, "CREATE TEMP TABLE qq_test (id int); INSERT INTO qq_test VALUES (1);")
	if len(r.Results) != 2 || r.Results[1].Command != "INSERT 0 1" {
		t.Fatal("missing command summaries")
	}
	r = exec(t, s, "BEGIN; INSERT INTO qq_test VALUES (2);")
	if r.TxStatus != 'T' {
		t.Fatal("transaction not retained")
	}
	if got := exec(t, s, "SELECT count(*) FROM qq_test").Results[0].Rows[0][0].Text; got != "2" {
		t.Fatal("session continuity lost")
	}
	exec(t, s, "ROLLBACK")
	if got := exec(t, s, "SELECT count(*) FROM qq_test").Results[0].Rows[0][0].Text; got != "1" {
		t.Fatal("rollback failed")
	}
	r = s.Execute(context.Background(), "INSERT INTO qq_test VALUES (3); SELECT 1/0;")
	if r.Err == nil {
		t.Fatal("expected batch failure")
	}
	if got := exec(t, s, "SELECT count(*) FROM qq_test").Results[0].Rows[0][0].Text; got != "1" {
		t.Fatal("batch was split or committed early")
	}
	exec(t, s, "BEGIN")
	r = s.Execute(context.Background(), "SELECT 1/0")
	if r.Err == nil || r.TxStatus != 'E' {
		t.Fatal("failed transaction state missing")
	}
	exec(t, s, "ROLLBACK")
}

func TestLimitsDrainAllResults(t *testing.T) {
	s := testSession(t)
	r := exec(t, s, "SELECT generate_series(1,1200); SELECT 99;")
	if !r.Truncated || len(r.Results[0].Rows) != MaxRows || r.Results[0].TotalRows != 1200 || r.Results[1].Rows[0][0].Text != "99" {
		t.Fatal("row cap lost counts or later results")
	}
	r = exec(t, s, "SELECT repeat('x',1048576) FROM generate_series(1,20); SELECT 42;")
	if !r.Truncated || len(r.Results[0].Rows) >= 16 || r.Results[len(r.Results)-1].Rows[0][0].Text != "42" {
		t.Fatal("byte cap or draining failed")
	}
	r = exec(t, s, "SELECT repeat('x',17000000); SELECT 7;")
	if !r.Truncated || len(r.Results[0].Rows) != 0 || r.Results[1].Rows[0][0].Text != "7" {
		t.Fatal("oversized cell handling failed")
	}
	exec(t, s, "SELECT 1")
}

func TestCancellationAndConnectionLoss(t *testing.T) {
	s := testSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	r := s.Execute(ctx, "SELECT pg_sleep(10)")
	if !errors.Is(r.Err, context.Canceled) || time.Since(start) > 4*time.Second {
		t.Fatalf("cancel failed: %+v", r)
	}
	if r.Disconnected {
		t.Fatal("normal cancellation should preserve session")
	}
	exec(t, s, "SELECT 1")
	r = s.Execute(context.Background(), "SELECT pg_terminate_backend(pg_backend_pid())")
	if r.Err == nil || !r.Disconnected {
		t.Fatal("connection loss not reported")
	}
}

func TestDollarQuotedSQLPassesThrough(t *testing.T) {
	s := testSession(t)
	r := exec(t, s, "DO $$ BEGIN PERFORM ';'; END $$; SELECT 'a;b' AS value; -- ;\nSELECT 3;")
	if len(r.Results) != 3 || r.Results[1].Rows[0][0].Text != "a;b" {
		t.Fatal("SQL batch was incorrectly split")
	}
}

func TestErrorText(t *testing.T) {
	if !strings.Contains(ErrorText(context.Canceled), "canceled") {
		t.Fatal("missing cancellation explanation")
	}
}
