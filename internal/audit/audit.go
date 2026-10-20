// Package audit implements the tamper-evident (not immutable) hash-chained audit log.
// hash = SHA256(prev_hash || seq || at || actor_id || action || target_type:target_id || canonical_json(meta)).
// There is no UPDATE or DELETE on audit_events anywhere in the code base (and DB triggers abort both).
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

const Genesis = "0000000000000000000000000000000000000000000000000000000000000000"

type Entry struct {
	ActorType, ActorID, ActorEmail, IP string
	Action, TargetType, TargetID       string
	Meta                               map[string]any
}

type Event struct {
	ID         string         `json:"id"`
	Seq        int64          `json:"seq"`
	At         string         `json:"at"`
	ActorType  string         `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	ActorEmail *string        `json:"actor_email"`
	IP         string         `json:"ip"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Meta       map[string]any `json:"meta"`
	PrevHash   string         `json:"prev_hash"`
	Hash       string         `json:"hash"`
}

// Canonical marshals with sorted keys and no HTML escaping (matches the mock's canonicalJson).
func Canonical(m map[string]any) string {
	if m == nil {
		m = map[string]any{}
	}
	raw, _ := json.Marshal(m)
	var v any
	_ = json.Unmarshal(raw, &v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // maps are emitted with sorted keys
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}

func ComputeHash(prev string, seq int64, at, actorID, action, targetType, targetID, metaJSON string) string {
	s := sha256.Sum256([]byte(fmt.Sprintf("%s%d%s%s%s%s:%s%s", prev, seq, at, actorID, action, targetType, targetID, metaJSON)))
	return hex.EncodeToString(s[:])
}

// Append writes one chained row inside the caller's transaction (same tx as the state change).
func Append(ctx context.Context, tx *sql.Tx, e Entry) (Event, error) {
	var lastSeq int64
	prev := Genesis
	err := tx.QueryRowContext(ctx, `SELECT seq, hash FROM audit_events ORDER BY seq DESC LIMIT 1`).Scan(&lastSeq, &prev)
	if err != nil && err != sql.ErrNoRows {
		return Event{}, err
	}
	if e.ActorType == "" {
		e.ActorType = "user"
	}
	if e.IP == "" {
		e.IP = "127.0.0.1"
	}
	now := time.Now().UTC()
	at := now.Format("2006-01-02T15:04:05.000Z")
	seq := lastSeq + 1
	meta := Canonical(e.Meta)
	h := ComputeHash(prev, seq, at, e.ActorID, e.Action, e.TargetType, e.TargetID, meta)
	id := crypto.ULID("aud_", now.UnixMilli())
	var email any
	var em *string
	if e.ActorEmail != "" {
		email = e.ActorEmail
		s := e.ActorEmail
		em = &s
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(seq,id,at,actor_type,actor_id,actor_email,ip,action,target_type,target_id,meta,prev_hash,hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, seq, id, at, e.ActorType, e.ActorID, email, e.IP, e.Action, e.TargetType, e.TargetID, meta, prev, h); err != nil {
		return Event{}, err
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(meta), &m)
	return Event{id, seq, at, e.ActorType, e.ActorID, em, e.IP, e.Action, e.TargetType, e.TargetID, m, prev, h}, nil
}

type Result struct {
	Valid           bool   `json:"valid"`
	VerifiedRows    int    `json:"verified_rows"`
	FirstInvalidSeq *int64 `json:"first_invalid_seq,omitempty"`
}

// Verify walks the chain from genesis. A flipped field, broken link or deleted row (seq gap) yields first_invalid_seq.
func Verify(ctx context.Context, db *sql.DB) (Result, error) {
	rows, err := db.QueryContext(ctx, `SELECT seq,at,actor_id,action,target_type,target_id,meta,prev_hash,hash FROM audit_events ORDER BY seq ASC`)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	prev, n, expect := Genesis, 0, int64(1)
	for rows.Next() {
		var seq int64
		var at, actor, action, tt, tid, meta, ph, h string
		if err := rows.Scan(&seq, &at, &actor, &action, &tt, &tid, &meta, &ph, &h); err != nil {
			return Result{}, err
		}
		if seq != expect || ph != prev || h != ComputeHash(prev, seq, at, actor, action, tt, tid, meta) {
			bad := seq
			if seq != expect {
				bad = expect
			}
			return Result{Valid: false, VerifiedRows: n, FirstInvalidSeq: &bad}, nil
		}
		prev, n, expect = h, n+1, expect+1
	}
	return Result{Valid: true, VerifiedRows: n}, rows.Err()
}
