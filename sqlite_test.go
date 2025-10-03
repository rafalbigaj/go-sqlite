// Copyright (c) 2018 David Crawshaw <david@zentus.com>
// Copyright (c) 2021 Roxy Light <roxy@zombiezen.com>
//
// Permission to use, copy, modify, and distribute this software for any
// purpose with or without fee is hereby granted, provided that the above
// copyright notice and this permission notice appear in all copies.
//
// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
// WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
// ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
// ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
// OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
//
// SPDX-License-Identifier: ISC

package sqlite_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"io"

	"modernc.org/libc"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func TestConn(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt, _, err := c.PrepareTransient("CREATE TABLE bartable (foo1 string, foo2 integer);")
	if err != nil {
		t.Fatal(err)
	}
	hasRow, err := stmt.Step()
	if err != nil {
		t.Fatal(err)
	}
	if hasRow {
		t.Errorf("CREATE TABLE reports having a row")
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	fooVals := []string{
		"bar",
		"baz",
		"bop",
	}

	for i, val := range fooVals {
		stmt, err := c.Prepare("INSERT INTO bartable (foo1, foo2) VALUES ($f1, $f2);")
		if err != nil {
			t.Fatal(err)
		}
		stmt.SetText("$f1", val)
		stmt.SetInt64("$f2", int64(i))
		hasRow, err = stmt.Step()
		if err != nil {
			t.Errorf("INSERT %q: %v", val, err)
		}
		if hasRow {
			t.Errorf("INSERT %q: has row", val)
		}
	}

	stmt, err = c.Prepare("SELECT foo1, foo2 FROM bartable;")
	if err != nil {
		t.Fatal(err)
	}
	gotVals := []string{}
	gotInts := []int64{}
	for {
		hasRow, err := stmt.Step()
		if err != nil {
			t.Errorf("SELECT: %v", err)
		}
		if !hasRow {
			break
		}
		val := stmt.ColumnText(0)
		if getVal := stmt.GetText("foo1"); getVal != val {
			t.Errorf(`GetText("foo1")=%q, want %q`, getVal, val)
		}
		intVal := stmt.ColumnInt64(1)
		if getIntVal := stmt.GetInt64("foo2"); getIntVal != intVal {
			t.Errorf(`GetText("foo2")=%q, want %q`, getIntVal, intVal)
		}
		typ := stmt.ColumnType(0)
		if typ != sqlite.TypeText {
			t.Errorf(`ColumnType(0)=%q, want %q`, typ, sqlite.TypeText)
		}
		intTyp := stmt.ColumnType(1)
		if intTyp != sqlite.TypeInteger {
			t.Errorf(`ColumnType(1)=%q, want %q`, intTyp, sqlite.TypeInteger)
		}
		gotVals = append(gotVals, val)
		gotInts = append(gotInts, intVal)
	}

	if !reflect.DeepEqual(fooVals, gotVals) {
		t.Errorf("SELECT foo1: got %v, want %v", gotVals, fooVals)
	}

	wantInts := []int64{0, 1, 2}
	if !reflect.DeepEqual(wantInts, gotInts) {
		t.Errorf("SELECT foo2: got %v, want %v", gotInts, wantInts)
	}

	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	stmt, err = c.Prepare(`SELECT "foo" = 'foo';`)
	if err == nil {
		stmt.Finalize()
		t.Error("Double-quoted string literals are permitted")
	}
}

func TestEarlyInterrupt(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	c.SetInterrupt(ctx.Done())

	stmt, _, err := c.PrepareTransient("CREATE TABLE bartable (foo1 string, foo2 integer);")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	stmt.Finalize()

	cancel()

	stmt, err = c.Prepare("INSERT INTO bartable (foo1, foo2) VALUES ($f1, $f2);")
	if err == nil {
		t.Fatal("Prepare err=nil, want prepare to fail")
	}
	if code := sqlite.ErrCode(err); code != sqlite.ResultInterrupt {
		t.Fatalf("Prepare err=%s, want SQLITE_INTERRUPT", code)
	}
}

func TestStmtInterrupt(t *testing.T) {
	conn, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmt := sqlite.InterruptedStmt(conn, "CREATE TABLE intt (c);")

	_, err = stmt.Step()
	if err == nil {
		t.Fatal("interrupted stmt Step succeeded")
	}
	if got := sqlite.ErrCode(err); got != sqlite.ResultInterrupt {
		t.Errorf("Step err=%v, want SQLITE_INTERRUPT", got)
	}
}

func TestInterruptStepReset(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	err = sqlitex.ExecScript(c, `CREATE TABLE resetint (c);
INSERT INTO resetint (c) VALUES (1);
INSERT INTO resetint (c) VALUES (2);
INSERT INTO resetint (c) VALUES (3);`)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.SetInterrupt(ctx.Done())

	stmt := c.Prep("SELECT * FROM resetint;")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	cancel()
	// next Step needs to reset stmt
	if _, err := stmt.Step(); sqlite.ErrCode(err) != sqlite.ResultInterrupt {
		t.Fatalf("want SQLITE_INTERRUPT, got %v", err)
	}
	c.SetInterrupt(nil)
	stmt = c.Prep("SELECT c FROM resetint ORDER BY c;")
	if _, err := stmt.Step(); err != nil {
		t.Fatalf("statement after interrupt reset failed: %v", err)
	}
	stmt.Reset()
}

func TestInterruptReset(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	err = sqlitex.ExecScript(c, `CREATE TABLE resetint (c);
INSERT INTO resetint (c) VALUES (1);
INSERT INTO resetint (c) VALUES (2);
INSERT INTO resetint (c) VALUES (3);`)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.SetInterrupt(ctx.Done())

	stmt := c.Prep("SELECT * FROM resetint;")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	cancel()
	c.SetInterrupt(nil)
	stmt = c.Prep("SELECT c FROM resetint ORDER BY c;")
	if _, err := stmt.Step(); err != nil {
		t.Fatalf("statement after interrupt reset failed: %v", err)
	}
	stmt.Reset()
}

func TestTrailingBytes(t *testing.T) {
	conn, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt, trailingBytes, err := conn.PrepareTransient("BEGIN; -- 56")
	if err != nil {
		t.Error(err)
	}
	stmt.Finalize()
	const want = 6
	if trailingBytes != want {
		t.Errorf("trailingBytes=%d, want %d", trailingBytes, want)
	}
}

func TestTrailingBytesError(t *testing.T) {
	conn, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}()

	if _, err := conn.Prepare("BEGIN; -- 56"); err == nil {
		t.Error("expecting error on trailing bytes")
	}
}

func TestBadParam(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt, err := c.Prepare("CREATE TABLE IF NOT EXISTS badparam (a, b, c);")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}

	stmt, err = c.Prepare("INSERT INTO badparam (a, b, c) VALUES ($a, $b, $c);")
	if err != nil {
		t.Fatal(err)
	}
	stmt.SetText("$a", "col_a")
	stmt.SetText("$b", "col_b")
	stmt.SetText("$badparam", "notaval")
	stmt.SetText("$c", "col_c")
	_, err = stmt.Step()
	if err == nil {
		t.Fatal("expecting error from bad param name, got no error")
	}
	if got := err.Error(); !strings.Contains(got, "$badparam") {
		t.Errorf(`error does not mention "$badparam": %v`, got)
	}
	stmt.Finalize()
}

func TestParallel(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt := c.Prep("CREATE TABLE testparallel (c);")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}

	stmt = c.Prep("INSERT INTO testparallel (c) VALUES ($c);")
	stmt.SetText("$c", "text")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	stmt.Reset()
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}

	stmt = c.Prep("SELECT * from testparallel;")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}

	stmt2 := c.Prep("SELECT count(*) from testparallel;")
	if hasRow, err := stmt2.Step(); err != nil {
		t.Fatal(err)
	} else if !hasRow {
		t.Error("expecting count row")
	}

	if hasRow, err := stmt.Step(); err != nil {
		t.Fatal(err)
	} else if !hasRow {
		t.Error("expecting second row")
	}
}

func TestBindBytes(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	val := make([]byte, 32)
	copy(val[5:], []byte("hello world"))

	stmt := c.Prep("CREATE TABLE IF NOT EXISTS bindbytes (c);")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	stmt = c.Prep("INSERT INTO bindbytes (c) VALUES ($bytes);")
	stmt.SetBytes("$bytes", val)
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}

	stmt = c.Prep("SELECT count(*) FROM bindbytes WHERE c = $bytes;")
	stmt.SetBytes("$bytes", val)
	if hasRow, err := stmt.Step(); err != nil {
		t.Fatal(err)
	} else if !hasRow {
		t.Error("SetBytes: result has no row")
	}
	if got := stmt.ColumnInt(0); got != 1 {
		t.Errorf("SetBytes: count is %d, want 1", got)
	}

	stmt.Reset()

	stmt.SetBytes("$bytes", val)
	if hasRow, err := stmt.Step(); err != nil {
		t.Fatal(err)
	} else if !hasRow {
		t.Error("SetBytes: result has no row")
	}
	if got := stmt.ColumnInt(0); got != 1 {
		t.Errorf("SetBytes: count is %d, want 1", got)
	}

	blob, err := c.OpenBlob("", "bindbytes", "c", 1, false)
	if err != nil {
		t.Fatalf("SetBytes: OpenBlob: %v", err)
	}
	defer func() {
		if err := blob.Close(); err != nil {
			t.Error(err)
		}
	}()

	storedVal, err := io.ReadAll(io.Reader(blob))
	if err != nil {
		t.Fatalf("SetBytes: Read: %v", err)
	}
	if !bytes.Equal(val, storedVal) {
		t.Fatalf("SetBytes: want: %x, got: %x", val, storedVal)
	}
}

func TestBindText(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	const val = "column_value"

	stmt := c.Prep("CREATE TABLE IF NOT EXISTS bindtext (c);")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	stmt = c.Prep("INSERT INTO bindtext (c) VALUES ($text);")
	stmt.SetText("$text", val)
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}

	stmt = c.Prep("SELECT count(*) FROM bindtext WHERE c = $text;")
	stmt.SetText("$text", val)
	if hasRow, err := stmt.Step(); err != nil {
		t.Fatal(err)
	} else if !hasRow {
		t.Error("SetText: result has no row")
	}
	if got := stmt.ColumnInt(0); got != 1 {
		t.Errorf("SetText: count is %d, want 1", got)
	}

	stmt.Reset()

	stmt.SetText("$text", val)
	if hasRow, err := stmt.Step(); err != nil {
		t.Fatal(err)
	} else if !hasRow {
		t.Error("SetText: result has no row")
	}
	if got := stmt.ColumnInt(0); got != 1 {
		t.Errorf("SetText: count is %d, want 1", got)
	}
}

func TestExtendedCodes(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt := c.Prep("CREATE TABLE IF NOT EXISTS extcodes (c UNIQUE);")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	stmt = c.Prep("INSERT INTO extcodes (c) VALUES ($c);")
	stmt.SetText("$c", "value1")
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	stmt.Reset()
	stmt.SetText("$c", "value1")
	_, err = stmt.Step()
	if err == nil {
		t.Fatal("expected UNIQUE error, got nothing")
	}
	if got, want := sqlite.ErrCode(err), sqlite.ResultConstraintUnique; got != want {
		t.Errorf("got err=%s, want %s", got, want)
	}
}

func TestSyntaxError(t *testing.T) {
	conn, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	stmt, _, err := conn.PrepareTransient(" \nSELECT );")
	if err == nil {
		stmt.Finalize()
		t.Fatal("No error returned")
	}

	msg := err.Error()
	t.Log("Message:", msg)
	if want := "2:8"; !strings.Contains(msg, want) {
		t.Errorf("err.Error() = %q; want to contain %q", msg, want)
	}

	got, ok := sqlite.ErrorOffset(err)
	if want := 9; got != want || ok == false {
		t.Errorf("ErrorOffset(err) = %d, %t; want %d, true", got, ok, want)
	}
}

func TestJournalMode(t *testing.T) {
	dir, err := os.MkdirTemp("", "crawshaw.io")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	tests := []struct {
		db    string
		mode  string
		flags sqlite.OpenFlags
	}{
		{
			"test-delete.db",
			"delete",
			sqlite.OpenReadWrite | sqlite.OpenCreate,
		},
		{
			"test-wal.db",
			"wal",
			sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenWAL,
		},
		{
			"test-default-wal.db",
			"wal",
			0,
		},
		// memory databases can't have wal, only journal_mode=memory
		{
			":memory:",
			"memory",
			0,
		},
		// temp databases can't have wal, only journal_mode=delete
		{
			"",
			"delete",
			0,
		},
	}

	for _, test := range tests {
		if test.db != ":memory:" && test.db != "" {
			test.db = filepath.Join(dir, test.db)
		}
		c, err := sqlite.OpenConn(test.db, test.flags)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := c.Close(); err != nil {
				t.Error(err)
			}
		}()
		stmt := c.Prep("PRAGMA journal_mode;")
		if hasRow, err := stmt.Step(); err != nil {
			t.Fatal(err)
		} else if !hasRow {
			t.Error("PRAGMA journal_mode: has no row")
		}
		if got := stmt.GetText("journal_mode"); got != test.mode {
			t.Errorf("journal_mode not set properly, got: %s, want: %s", got, test.mode)
		}
	}
}

func TestBusyTimeout(t *testing.T) {
	dir, err := os.MkdirTemp("", "crawshaw.io")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	db := filepath.Join(dir, "busytest.db")

	flags := sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenWAL

	conn0, err := sqlite.OpenConn(db, flags)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn0.Close(); err != nil {
			t.Error(err)
		}
	}()

	conn1, err := sqlite.OpenConn(db, flags)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn1.Close(); err != nil {
			t.Error(err)
		}
	}()

	err = sqlitex.ExecScript(conn0, `
		CREATE TABLE t (c);
		INSERT INTO t (c) VALUES (1);
	`)
	if err != nil {
		t.Fatal(err)
	}

	c0Lock := func() {
		if _, err := conn0.Prep("BEGIN;").Step(); err != nil {
			t.Fatal(err)
		}
		if _, err := conn0.Prep("INSERT INTO t (c) VALUES (2);").Step(); err != nil {
			t.Fatal(err)
		}
	}
	c0Unlock := func() {
		if _, err := conn0.Prep("COMMIT;").Step(); err != nil {
			t.Fatal(err)
		}
	}

	c0Lock()
	done := make(chan struct{})
	go func() {
		_, err = conn1.Prep("INSERT INTO t (c) VALUES (3);").Step()
		if err != nil {
			t.Errorf("insert failed: %v", err)
		}
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	select {
	case <-done:
		t.Errorf("done before unlock")
	default:
	}

	c0Unlock()
	<-done

	c0Lock()
	done = make(chan struct{})
	go func() {
		conn1.SetBusyTimeout(5 * time.Millisecond)
		_, err = conn1.Prep("INSERT INTO t (c) VALUES (4);").Step()
		if sqlite.ErrCode(err) != sqlite.ResultBusy {
			t.Errorf("want SQLITE_BUSY got %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Errorf("short busy timeout got stuck")
	}

	c0Unlock()
	<-done
}

func TestBlockOnBusy(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "busytest.db")
	const flags = sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenWAL

	conn0, err := sqlite.OpenConn(db, flags)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn0.Close(); err != nil {
			t.Error(err)
		}
	}()

	conn1, err := sqlite.OpenConn(db, flags)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn1.Close(); err != nil {
			t.Error(err)
		}
	}()

	if _, err := conn0.Prep("BEGIN EXCLUSIVE;").Step(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn1.SetInterrupt(ctx.Done())
	_, err = conn1.Prep("BEGIN EXCLUSIVE;").Step()
	cancel()
	if code := sqlite.ErrCode(err).ToPrimary(); code != sqlite.ResultBusy {
		t.Errorf("Concurrent transaction error: %v (code=%v); want code=%v", err, code, sqlite.ResultBusy)
	}
}

func TestColumnIndex(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt, err := c.Prepare("CREATE TABLE IF NOT EXISTS columnindex (a, b, c);")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}

	stmt, err = c.Prepare("SELECT b, 1 AS d, a, c FROM columnindex")
	if err != nil {
		t.Fatal(err)
	}

	cols := []struct {
		name string
		idx  int
	}{
		{"a", 2},
		{"b", 0},
		{"c", 3},
		{"d", 1},
		{"badcol", -1},
	}

	for _, col := range cols {
		if got := stmt.ColumnIndex(col.name); got != col.idx {
			t.Errorf("expected column %s to have index %d, got %d", col.name, col.idx, got)
		}
	}

	stmt.Finalize()
}

func TestBindParamName(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt, err := c.Prepare("SELECT :foo, :bar;")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Finalize()

	var got []string
	for i, n := 1, stmt.BindParamCount(); i <= n; i++ {
		got = append(got, stmt.BindParamName(i))
	}
	// We don't care what indices SQLite picked, so sort returned names.
	sort.Strings(got)
	want := []string{":bar", ":foo"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("names = %q; want %q", got, want)
	}
}

// Just to verify that the JSON1 extension is automatically loaded.
func TestJSON1Extension(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	got, err := sqlitex.ResultText(c.Prep(`SELECT json(' { "this" : "is", "a": [ "test" ] } ');`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"this":"is","a":["test"]}`; got != want {
		t.Errorf("json(...) = %q; want %q", got, want)
	}
}

func TestLimit(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	c.Limit(sqlite.LimitSQLLength, 1)
	stmt, err := c.Prepare("SELECT 1;")
	if err == nil {
		stmt.Finalize()
		t.Fatal("Prepare did not return an error")
	}
	if got, want := sqlite.ErrCode(err), sqlite.ResultTooBig; got != want {
		t.Errorf("sqlite.ErrCode(err) = %v; want %v", got, want)
	}
}

func TestSetDefensive(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	err = sqlitex.ExecTransient(c, `PRAGMA writable_schema=ON;`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetDefensive(true); err != nil {
		t.Error("SetDefensive:", err)
	}
	err = sqlitex.ExecTransient(c,
		`INSERT INTO sqlite_schema (type, name, tbl_name, sql) `+
			`VALUES ('table','foo','foo','CREATE TABLE foo (id integer primary key)');`,
		nil,
	)
	if err == nil {
		t.Fatal("Inserting into sqlite_schema did not return an error")
	} else {
		t.Log("Insert sqlite_schema:", err)
	}
}

func TestSerialize(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	err = sqlitex.ExecTransient(c, `CREATE TABLE foo (msg TEXT NOT NULL);`, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = sqlitex.ExecTransient(c, `INSERT INTO foo VALUES ('Hello, World!');`, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.Serialize("main")
	if err != nil {
		t.Fatal("Serialize:", err)
	}

	err = sqlitex.ExecTransient(c, `ATTACH DATABASE ':memory:' AS a;`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Deserialize("a", data); err != nil {
		t.Error("Deserialize:", err)
	}

	const want = "Hello, World!"
	var nResults int
	err = sqlitex.ExecTransient(c, `SELECT msg FROM a.foo;`, func(stmt *sqlite.Stmt) error {
		nResults++
		if got := stmt.ColumnText(0); got != want {
			t.Errorf("msg = %q; want %q", got, want)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
	if nResults != 1 {
		t.Errorf("COUNT(*) = %d; want 1", nResults)
	}
}

func TestForeignKey(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	err = sqlitex.ExecuteTransient(c, `PRAGMA foreign_keys = on;`, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = sqlitex.ExecuteScript(c, `CREATE TABLE artist(
  artistid    INTEGER PRIMARY KEY,
  artistname  TEXT
);
CREATE TABLE track(
  trackid     INTEGER,
  trackname   TEXT,
  trackartist INTEGER,
  FOREIGN KEY(trackartist) REFERENCES artist(artistid)
);`, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = sqlitex.ExecuteTransient(c, `INSERT INTO track VALUES(14, 'Mr. Bojangles', 3);`, nil)
	if err == nil {
		t.Fatal("No error from breaking foreign key")
	} else {
		t.Log("Got (intentional) error:", err)
	}
}

func TestMain(m *testing.M) {
	_ = libc.Environ() // Forces libc.SetEnviron; fixes memory accounting balance for environ(7).
	libc.MemAuditStart()
	rc := m.Run()
	if err := libc.MemAuditReport(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		rc = 1
	}
	os.Exit(rc)
}

func TestStmtResetInterrupted(t *testing.T) {
	// Open an in-memory database
	conn, err := sqlite.OpenConn(":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer conn.Close()

	// Create a test table
	stmt, err := conn.Prepare("CREATE TABLE test (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatalf("failed to prepare statement: %v", err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	stmt.Finalize()

	// Prepare a statement that will be interrupted
	stmt, err = conn.Prepare("SELECT * FROM test")
	if err != nil {
		t.Fatalf("failed to prepare statement: %v", err)
	}
	defer stmt.Finalize()

	// Set interrupt channel and close it immediately to simulate interruption
	doneCh := make(chan struct{})
	conn.SetInterrupt(doneCh)
	close(doneCh)

	// Wait a bit to ensure interrupt is processed
	time.Sleep(10 * time.Millisecond)

	// Attempt to reset the statement - this should not panic
	err = stmt.Reset()
	if err == nil {
		t.Error("expected error from Reset when interrupted, got nil")
	} else if got, want := sqlite.ErrCode(err), sqlite.ResultInterrupt; got != want {
		t.Errorf("expected result interrupt error: %v, but got sqlite.ErrCode(err) = %v", want, got)
	}
}
