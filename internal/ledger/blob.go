package ledger

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// Blob kinds.
const (
	BlobPrompt   = "prompt"
	BlobEnvelope = "envelope"
	BlobOutput   = "output"
)

// PutBlob stores content addressed by its own hash and returns that hash.
//
// This is what makes a past run reproducible rather than merely described. The
// run row records a prompt digest and an envelope digest; without the bytes
// behind them, "we know what this run was told" is a claim about a hash nobody
// can check. With them, a run from three weeks ago can be re-decided today
// against exactly the text it was given.
//
// Storage is content-addressed, so identical prompts across hundreds of runs
// cost one row, and a blob is never updated — the database refuses it,
// because changing the content under a hash is the one thing that would make
// the whole scheme a lie.
func (l *Ledger) PutBlob(kind string, content []byte) (string, error) {
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	_, err := l.db.Exec(
		`INSERT INTO adlc_blob(sha,kind,bytes,ts_ms) VALUES(?,?,?,?)
		 ON CONFLICT(sha) DO NOTHING`,
		sha, kind, string(content), l.now().UnixMilli())
	if err != nil {
		return "", fmt.Errorf("store %s blob: %w", kind, err)
	}
	return sha, nil
}

// Blob reads stored content back.
func (l *Ledger) Blob(sha string) ([]byte, error) {
	if sha == "" {
		return nil, fmt.Errorf("no digest given: %w", ErrNotFound)
	}
	var body string
	err := l.db.QueryRow(`SELECT bytes FROM adlc_blob WHERE sha=?`, sha).Scan(&body)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("blob %s was not retained: %w", short(sha), ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return []byte(body), nil
}

// HasBlob reports whether content was retained.
func (l *Ledger) HasBlob(sha string) bool {
	if sha == "" {
		return false
	}
	var n int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM adlc_blob WHERE sha=?`, sha).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// blobIntegrity re-hashes every stored blob against the key it is filed under.
//
// It belongs in the integrity tier rather than the knowledge tier: a blob
// whose contents no longer hash to its own address has been altered, and every
// run citing it is describing something other than what it was given.
func (l *Ledger) blobIntegrity() (bad int, total int, err error) {
	rows, err := l.db.Query(`SELECT sha, bytes FROM adlc_blob`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var sha, body string
		if err := rows.Scan(&sha, &body); err != nil {
			return 0, 0, err
		}
		total++
		sum := sha256.Sum256([]byte(body))
		if hex.EncodeToString(sum[:]) != sha {
			bad++
		}
	}
	return bad, total, rows.Err()
}

// BlobStats reports retention, so a report can say plainly how much of the
// record is reproducible and how much is only described.
func (l *Ledger) BlobStats() (blobs int, bytes int64, err error) {
	err = l.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(LENGTH(bytes)),0) FROM adlc_blob`).Scan(&blobs, &bytes)
	return blobs, bytes, err
}
