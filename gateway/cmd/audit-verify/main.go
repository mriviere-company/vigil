// Command audit-verify checks a vigil audit log's hash chain (see
// gateway/internal/audit's package doc for the chaining design). It reads a
// JSONL file, checks the first record is the chain's genesis anchor (seq 1,
// prev_hash "genesis" — without this, deleting the oldest records would
// verify clean), recomputes every record's hash, checks each record's
// prev_hash against the previous record's hash, and checks that seq is
// gapless.
//
// What that detects: modification of any field, reordering, interior
// deletion, and prefix deletion (oldest records removed). What it cannot
// detect from the file alone: tail truncation — cutting records off the END
// of the file leaves a shorter but internally valid chain, and no
// single-file scheme can tell "the log simply ends here" from "someone
// removed the newest records." An operator who needs that property should
// record the latest record_hash out of band (a high-water-mark) and compare
// it against the file's last record. See gateway/internal/audit's package
// doc.
//
// Usage: go run ./cmd/audit-verify <path-to-audit.jsonl>
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"

	"vigil-api/gateway/internal/audit"
)

// result is verify's structured outcome, kept separate from stdout/stderr so
// tests can assert on it directly instead of capturing printed output.
type result struct {
	ok      bool
	n       int    // records verified before the first break (or all, if ok)
	message string // populated only when !ok: describes the first broken link
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: audit-verify <path-to-audit.jsonl>")
		os.Exit(2)
	}

	res, err := verify(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if !res.ok {
		fmt.Println(res.message)
		os.Exit(1)
	}
	fmt.Printf("OK %d records\n", res.n)
}

// verify reads path line by line, verifying record_hash and the prev_hash/seq
// chain. It stops at the first broken link (there is no value in reporting
// past that point — one edit invalidates everything after it).
func verify(path string) (result, error) {
	f, err := os.Open(path)
	if err != nil {
		return result{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10<<20)

	var prev *audit.Record
	n := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		rec, err := audit.ParseRecord(line)
		if err != nil {
			return result{n: n, message: fmt.Sprintf("broken link at record %d: invalid JSON: %v", n+1, err)}, nil
		}
		if err := audit.VerifyRecordHash(rec); err != nil {
			return result{n: n, message: fmt.Sprintf("broken link at record %d (seq %d): %v", n+1, rec.Seq, err)}, nil
		}
		if prev == nil {
			// The first record must be the chain's genesis anchor. Without
			// this check, deleting the oldest N lines would leave a file whose
			// remaining records are individually valid and mutually chained —
			// prefix deletion would verify clean.
			if rec.Seq != 1 || rec.PrevHash != "genesis" {
				return result{n: n, message: fmt.Sprintf(
					"broken link at record 1 (seq %d, prev_hash %q): chain not anchored to genesis — records before this one were removed",
					rec.Seq, rec.PrevHash)}, nil
			}
		}
		if prev != nil {
			if rec.PrevHash != prev.RecordHash {
				return result{n: n, message: fmt.Sprintf(
					"broken link at record %d (seq %d): prev_hash %q does not match the previous record's hash %q",
					n+1, rec.Seq, rec.PrevHash, prev.RecordHash)}, nil
			}
			if rec.Seq != prev.Seq+1 {
				return result{n: n, message: fmt.Sprintf(
					"broken link at record %d: seq %d is not the expected %d", n+1, rec.Seq, prev.Seq+1)}, nil
			}
		}
		prev = &rec
		n++
	}
	if err := scanner.Err(); err != nil {
		return result{}, fmt.Errorf("read %s: %w", path, err)
	}
	return result{ok: true, n: n}, nil
}
