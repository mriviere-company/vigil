package audit

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

type mockMetrics struct {
	mu          sync.Mutex
	records     int
	dropped     int
	writeErrors int
}

func (m *mockMetrics) IncAuditRecord() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records++
}

func (m *mockMetrics) IncAuditDropped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped++
}

func (m *mockMetrics) IncAuditWriteError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeErrors++
}

func (m *mockMetrics) snapshot() (records, dropped, writeErrors int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.records, m.dropped, m.writeErrors
}

func readAllRecords(t *testing.T, path string) []Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var out []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		rec, err := ParseRecord(line)
		if err != nil {
			t.Fatalf("ParseRecord: %v (line %q)", err, line)
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// assertValidChain checks every record's hash and prev_hash linkage, and that
// seq is a gapless sequence starting at 1.
func assertValidChain(t *testing.T, recs []Record) {
	t.Helper()
	prevHash := "genesis"
	for i, rec := range recs {
		if err := VerifyRecordHash(rec); err != nil {
			t.Errorf("record %d (seq %d): %v", i, rec.Seq, err)
		}
		if rec.Seq != uint64(i+1) {
			t.Errorf("record %d: seq = %d, want %d", i, rec.Seq, i+1)
		}
		if rec.PrevHash != prevHash {
			t.Errorf("record %d (seq %d): prev_hash = %q, want %q", i, rec.Seq, rec.PrevHash, prevHash)
		}
		prevHash = rec.RecordHash
	}
}

func TestChainCorrectness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	m := &mockMetrics{}
	l, err := NewLogger(path, testLogger(), m)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	for i := 0; i < 5; i++ {
		l.Append(Entry{
			Timestamp:  time.Now(),
			RequestID:  "rid",
			APIKeyHash: "keyhash",
			Endpoint:   "/v1/moderate/text",
			SoR:        testSoR(),
		})
	}
	l.Close()

	recs := readAllRecords(t, path)
	if len(recs) != 5 {
		t.Fatalf("got %d records, want 5", len(recs))
	}
	assertValidChain(t, recs)

	if records, dropped, writeErrors := m.snapshot(); records != 5 || dropped != 0 || writeErrors != 0 {
		t.Errorf("metrics = records:%d dropped:%d writeErrors:%d, want 5/0/0", records, dropped, writeErrors)
	}
}

// TestDropOnFullChannelDoesNotBlock is a white-box test: it builds a Logger
// directly (bypassing NewLogger, so no writer goroutine drains the channel)
// with a capacity-1 channel, fills it, then proves a further Append returns
// immediately (non-blocking) and increments the dropped counter instead of
// writing.
func TestDropOnFullChannelDoesNotBlock(t *testing.T) {
	m := &mockMetrics{}
	l := &Logger{
		ch:      make(chan Entry, 1),
		done:    make(chan struct{}),
		logger:  testLogger(),
		metrics: m,
	}
	l.ch <- Entry{RequestID: "fills-the-buffer"} // channel now full; nothing drains it

	done := make(chan struct{})
	go func() {
		l.Append(Entry{RequestID: "should-be-dropped"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Append blocked on a full channel instead of dropping")
	}

	if records, dropped, _ := m.snapshot(); records != 0 || dropped != 1 {
		t.Errorf("metrics = records:%d dropped:%d, want 0/1", records, dropped)
	}
}

// TestConcurrentProducersHammer exercises many goroutines calling Append
// concurrently (run with -race). Regardless of how many records the bounded
// channel had room for, every record that DID land must form a fully valid,
// gapless hash chain — proving the single-writer design serializes seq/hash
// assignment correctly under contention.
func TestConcurrentProducersHammer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	m := &mockMetrics{}
	l, err := NewLogger(path, testLogger(), m)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	const goroutines = 50
	const perGoroutine = 20
	const total = goroutines * perGoroutine

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				l.Append(Entry{
					Timestamp:  time.Now(),
					RequestID:  "hammer",
					APIKeyHash: "keyhash",
					Endpoint:   "/v1/moderate/text",
					SoR:        testSoR(),
				})
			}
		}(g)
	}
	wg.Wait()
	l.Close()

	recs := readAllRecords(t, path)
	assertValidChain(t, recs)

	records, dropped, _ := m.snapshot()
	if records != len(recs) {
		t.Errorf("metrics records = %d, but file has %d records", records, len(recs))
	}
	if records+dropped != total {
		t.Errorf("records+dropped = %d, want %d (total attempted)", records+dropped, total)
	}
}

// TestCloseDrainsQueuedEntries proves the graceful-shutdown drain guarantee:
// entries queued right before Close must all be flushed before Close returns,
// with no explicit synchronization from the caller.
func TestCloseDrainsQueuedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	m := &mockMetrics{}
	l, err := NewLogger(path, testLogger(), m)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	const n = 50
	for i := 0; i < n; i++ {
		l.Append(Entry{Timestamp: time.Now(), RequestID: "queued", APIKeyHash: "k", Endpoint: "/v1/moderate/text", SoR: testSoR()})
	}
	l.Close() // no sleep beforehand: Close itself must drain

	recs := readAllRecords(t, path)
	if len(recs) != n {
		t.Fatalf("got %d records after Close, want %d (drain incomplete)", len(recs), n)
	}
	assertValidChain(t, recs)
}

func TestNewLoggerInvalidPathErrors(t *testing.T) {
	_, err := NewLogger(filepath.Join(t.TempDir(), "no-such-dir", "audit.jsonl"), testLogger(), nil)
	if err == nil {
		t.Fatal("expected an error opening a path in a nonexistent directory")
	}
}

func TestNewLoggerNilMetricsDefaultsToNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := NewLogger(path, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.Append(Entry{Timestamp: time.Now(), SoR: testSoR()})
	l.Close() // must not panic despite nil metrics
}

// TestNoopMetricsIsSafe pins the no-op MetricsRecorder's contract: every
// method is callable and does nothing.
func TestNoopMetricsIsSafe(t *testing.T) {
	var m noopMetrics
	m.IncAuditRecord()
	m.IncAuditDropped()
	m.IncAuditWriteError()
}

// TestRunHashFailureDropsRecordAndPreservesChain: JSON cannot encode NaN/Inf,
// so a record whose scores somehow contain one (defensive — see run's
// comment) must be logged and dropped rather than corrupt the file, and must
// not consume a seq number so the surviving chain stays gapless.
func TestRunHashFailureDropsRecordAndPreservesChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	m := &mockMetrics{}
	l, err := NewLogger(path, testLogger(), m)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	unhashable := testSoR()
	unhashable.Detection.Scores = map[string]float64{"toxicity": math.NaN()}
	l.Append(Entry{Timestamp: time.Now(), RequestID: "bad", SoR: unhashable})
	l.Append(Entry{Timestamp: time.Now(), RequestID: "good", SoR: testSoR()})
	l.Close()

	recs := readAllRecords(t, path)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1 (the unhashable entry must be dropped, not written)", len(recs))
	}
	if recs[0].Seq != 1 {
		t.Errorf("surviving record seq = %d, want 1 (a dropped entry must not consume a seq number)", recs[0].Seq)
	}
	if recs[0].PrevHash != "genesis" {
		t.Errorf("surviving record prev_hash = %q, want genesis", recs[0].PrevHash)
	}
	assertValidChain(t, recs)
	if records, dropped, writeErrors := m.snapshot(); records != 1 || dropped != 0 || writeErrors != 1 {
		t.Errorf("metrics = records:%d dropped:%d writeErrors:%d, want 1/0/1 (a hash failure counts as a write error, not a queue-full drop)",
			records, dropped, writeErrors)
	}
}

// errWriter always fails, simulating a disk-full/read-only-fs condition.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// TestRunWriteFailureDropsRecord forces the underlying writer to fail on the
// very first byte (a 1-byte bufio buffer means any real record forces an
// immediate write-through), proving the write-error branch logs and drops
// rather than panicking or wedging the goroutine.
func TestRunWriteFailureDropsRecord(t *testing.T) {
	m := &mockMetrics{}
	l := &Logger{ch: make(chan Entry, 4), done: make(chan struct{}), logger: testLogger(), metrics: m}
	go l.run(bufio.NewWriterSize(errWriter{errors.New("disk full")}, 1), nil)

	l.Append(Entry{Timestamp: time.Now(), RequestID: "r1", SoR: testSoR()})
	l.Close()

	if records, dropped, writeErrors := m.snapshot(); records != 0 || dropped != 0 || writeErrors != 1 {
		t.Errorf("metrics = records:%d dropped:%d writeErrors:%d, want 0/0/1 (a write failure must be counted, not just logged)",
			records, dropped, writeErrors)
	}
}

// TestRunFlushFailureDropsRecord uses a normal-size buffer (so Write itself
// just buffers the small record in memory and succeeds) and relies on the
// explicit per-record Flush to surface the underlying failure.
func TestRunFlushFailureDropsRecord(t *testing.T) {
	m := &mockMetrics{}
	l := &Logger{ch: make(chan Entry, 4), done: make(chan struct{}), logger: testLogger(), metrics: m}
	go l.run(bufio.NewWriter(errWriter{errors.New("read-only filesystem")}), nil)

	l.Append(Entry{Timestamp: time.Now(), RequestID: "r1", SoR: testSoR()})
	l.Close()

	if records, dropped, writeErrors := m.snapshot(); records != 0 || dropped != 0 || writeErrors != 1 {
		t.Errorf("metrics = records:%d dropped:%d writeErrors:%d, want 0/0/1 (a flush failure must be counted, not just logged)",
			records, dropped, writeErrors)
	}
}
