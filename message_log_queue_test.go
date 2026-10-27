package main

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

type queueTestDriver struct{}

type queueTestState struct {
	mu           sync.Mutex
	failNextExec bool
	commits      int
}

var queueTestStateValue *queueTestState

func (queueTestDriver) Open(string) (driver.Conn, error) {
	return queueTestConn{}, nil
}

type queueTestConn struct{}

func (queueTestConn) Prepare(string) (driver.Stmt, error) {
	return queueTestStmt{}, nil
}
func (queueTestConn) Close() error { return nil }
func (queueTestConn) Begin() (driver.Tx, error) {
	return queueTestTx{}, nil
}

type queueTestStmt struct{}

func (queueTestStmt) Close() error  { return nil }
func (queueTestStmt) NumInput() int { return -1 }
func (queueTestStmt) Exec([]driver.Value) (driver.Result, error) {
	queueTestStateValue.mu.Lock()
	defer queueTestStateValue.mu.Unlock()
	if queueTestStateValue.failNextExec {
		queueTestStateValue.failNextExec = false
		return nil, errors.New("injected insert failure")
	}
	return driver.RowsAffected(1), nil
}
func (queueTestStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("unexpected query")
}

type queueTestTx struct{}

func (queueTestTx) Commit() error {
	queueTestStateValue.mu.Lock()
	queueTestStateValue.commits++
	queueTestStateValue.mu.Unlock()
	return nil
}
func (queueTestTx) Rollback() error { return nil }

var registerQueueTestDriver sync.Once

func openQueueTestDB(t *testing.T, state *queueTestState) *sql.DB {
	t.Helper()
	registerQueueTestDriver.Do(func() { sql.Register("sunfire-queue-test", queueTestDriver{}) })
	queueTestStateValue = state
	db, err := sql.Open("sunfire-queue-test", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func resetMessageLogQueue(t *testing.T) {
	t.Helper()
	msgQueueMu.Lock()
	oldQueue, oldFlushing := msgQueue, isFlushing
	msgQueue = nil
	isFlushing = false
	msgQueueMu.Unlock()
	t.Cleanup(func() {
		msgQueueMu.Lock()
		msgQueue, isFlushing = oldQueue, oldFlushing
		msgQueueMu.Unlock()
	})
}

func TestFlushMessageLogQueueRetainsMessagesWhenPostgresUnavailable(t *testing.T) {
	resetMessageLogQueue(t)
	oldProvider, oldDelay := messageLogDBProvider, messageLogRetryDelay
	messageLogDBProvider = func() *sql.DB { return nil }
	messageLogRetryDelay = time.Hour
	t.Cleanup(func() {
		messageLogDBProvider = oldProvider
		messageLogRetryDelay = oldDelay
	})

	msgQueueMu.Lock()
	msgQueue = []*loggedMessageRow{{ID: 1}}
	isFlushing = true
	msgQueueMu.Unlock()
	flushMessageLogQueue()

	msgQueueMu.Lock()
	defer msgQueueMu.Unlock()
	if len(msgQueue) != 1 || msgQueue[0].ID != 1 {
		t.Fatalf("queue after unavailable DB = %#v; expected queued message to remain", msgQueue)
	}
	if isFlushing {
		t.Fatal("isFlushing remained true after unavailable DB")
	}
}

func TestFlushMessageLogQueueRequeuesFailureAndRetries(t *testing.T) {
	resetMessageLogQueue(t)
	oldProvider, oldDelay := messageLogDBProvider, messageLogRetryDelay
	messageLogRetryDelay = time.Hour
	state := &queueTestState{failNextExec: true}
	db := openQueueTestDB(t, state)
	messageLogDBProvider = func() *sql.DB { return db }
	t.Cleanup(func() {
		messageLogDBProvider = oldProvider
		messageLogRetryDelay = oldDelay
	})

	msgQueueMu.Lock()
	msgQueue = []*loggedMessageRow{{ID: 2}}
	isFlushing = true
	msgQueueMu.Unlock()
	flushMessageLogQueue()

	msgQueueMu.Lock()
	retained := len(msgQueue) == 1 && msgQueue[0].ID == 2
	isFlushing = true
	msgQueueMu.Unlock()
	if !retained {
		t.Fatal("transaction failure did not requeue the batch")
	}

	flushMessageLogQueue()
	msgQueueMu.Lock()
	defer msgQueueMu.Unlock()
	if len(msgQueue) != 0 {
		t.Fatalf("queue after successful retry = %#v", msgQueue)
	}
	if isFlushing {
		t.Fatal("isFlushing remained true after successful flush")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.commits != 1 {
		t.Fatalf("commits = %d; want 1 after retry", state.commits)
	}
}
