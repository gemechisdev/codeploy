// codeplo: break-glass CLI. Operates directly on the data directory (run as the service user).
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/authn"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules/hardening"
	"github.com/codeplo/codeplo/internal/modules/terminal"
	"github.com/codeplo/codeplo/internal/platform"
	"github.com/codeplo/codeplo/internal/store"
)

// changeIDRe guards the id used as a spool directory name: no separators, so a CLI argument can never escape
// the snapshot directory.
var changeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: codeplo doctor [--verify-audit] | reset-password --email E | disable-2fa --email E | status [--hardening] | setup-token | revert <changeId|all> [--list] | cron list|add|remove|history | templates list|check | previews list|check | github-app check|repos | pty-helper (systemd only)")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
	// pty-helper runs as root, one process per connection, started by codeplo-pty@.service. It must not touch the
	// data directory or the database: its whole policy is the peer-uid check and terminal.Authorize.
	if cmd == "pty-helper" {
		return terminal.ServeHelperStdin(terminal.DefaultHelperPolicy())
	}
	// cron owns its arguments: it takes flags of its own and an argv after `--`, so a command word like `find
	// /data -mtime +30 -delete` must reach the sub-command untouched. Dispatching it here keeps the shared flag set
	// below from parsing half of it and exiting on the rest.
	if cmd == "cron" {
		return cronCommand(args)
	}
	// templates takes its own flags too, and it reads files rather than the database, so it never needs the data
	// directory opened at all.
	if cmd == "templates" {
		return templatesCommand(args)
	}
	// github-app reads the environment the daemon will be started with and talks to GitHub; it touches neither the
	// data directory nor the database, so it answers "is this App usable?" before anything depends on it.
	if cmd == "github-app" {
		return githubAppCommand(args)
	}
	// previews reads the database and the environment and writes nothing: taking a preview down is the daemon's sweep,
	// on the process that owns the engine and the containers. It opens the data directory itself, so it is dispatched
	// before the shared flag set like the other reader commands.
	if cmd == "previews" {
		return previewsCommand(args)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	email := fs.String("email", "", "account email")
	verify := fs.Bool("verify-audit", false, "verify the audit hash chain")
	spoolFlag := fs.String("revert-dir", "", "hardening snapshot spool (default $CODEPLO_REVERT_DIR, else <data-dir>/revert)")
	list := fs.Bool("list", false, "with revert: list pending/unconfirmed changes and their confirm deadlines")
	withHardening := fs.Bool("hardening", false, "with status: open hardening changes, deadlines and spool state")
	_ = fs.Parse(args)
	spool := revertSpool(cfg, *spoolFlag)

	// revert is the body of the systemd dead-man timer: it restores the spool from disk and opens the database
	// only afterwards, so a locked, missing or un-migrated database can never keep the host locked out. Every
	// other command needs the database up front.
	db := (*sql.DB)(nil)
	if cmd != "revert" {
		opened, key, err := app.Open(cfg)
		_ = key
		if err != nil {
			return err
		}
		defer opened.Close()
		db = opened
	}

	switch cmd {
	case "doctor":
		var mode string
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			return err
		}
		fmt.Printf("ok  database %s (journal_mode=%s)\nok  master key %s\n", cfg.DBPath(), mode, cfg.MasterKeyPath())
		if *verify {
			res, err := audit.Verify(ctx, db)
			if err != nil {
				return err
			}
			if !res.Valid {
				return fmt.Errorf("audit chain INVALID: first_invalid_seq=%d after %d verified rows", *res.FirstInvalidSeq, res.VerifiedRows)
			}
			fmt.Printf("ok  audit chain verified (%d rows)\n", res.VerifiedRows)
		}
	case "revert":
		return revert(ctx, cfg, spool, *list, fs.Args())
	case "status":
		var users, sess, rows int
		var step string
		_ = db.QueryRow(`SELECT (SELECT COUNT(*) FROM users),(SELECT COUNT(*) FROM sessions WHERE pending_challenge IS NULL),(SELECT COUNT(*) FROM audit_events),(SELECT step FROM setup_state WHERE id=1)`).Scan(&users, &sess, &rows, &step)
		fmt.Printf("version=%s setup_step=%s users=%d sessions=%d audit_rows=%d\n", app.Version, step, users, sess, rows)
		if *withHardening {
			if err := statusHardening(ctx, db, spool); err != nil {
				return err
			}
		}
	case "setup-token":
		tok, err := app.IssueSetupToken(ctx, db)
		if err != nil {
			return err
		}
		fmt.Printf("One-time setup token (valid 1 hour, single use):\n%s\nssh -L 7443:127.0.0.1:7443 <host>  then open http://127.0.0.1:7443/setup?token=%s\n", tok, tok)
	case "reset-password":
		if *email == "" {
			return fmt.Errorf("--email is required")
		}
		fmt.Fprint(os.Stderr, "New password (min 12 chars, read from stdin): ")
		pw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		pw = strings.TrimRight(pw, "\r\n")
		if len(pw) < authn.MinPasswordLen {
			return fmt.Errorf("password must be at least %d characters", authn.MinPasswordLen)
		}
		h, err := authn.HashPassword(pw)
		if err != nil {
			return err
		}
		return userChange(ctx, db, *email, "cli.reset_password", `UPDATE users SET password_hash=? WHERE id=?`, h)
	case "disable-2fa":
		if *email == "" {
			return fmt.Errorf("--email is required")
		}
		return userChange(ctx, db, *email, "cli.disable_2fa", `UPDATE users SET totp_enabled=0, totp_secret_enc=NULL WHERE id=?`)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

// userChange applies an update, revokes the user's sessions and writes an audit row, all in one transaction.
func userChange(ctx context.Context, db *sql.DB, email, action, q string, extra ...any) error {
	return store.InTx(ctx, db, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRow(`SELECT id FROM users WHERE email=?`, email).Scan(&id); err != nil {
			return fmt.Errorf("no such user")
		}
		if _, err := tx.Exec(q, append(extra, id)...); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id=?`, id); err != nil {
			return err
		}
		if _, err := audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "cli", Action: action, TargetType: "user", TargetID: id}); err != nil {
			return err
		}
		fmt.Println("done; all sessions for the account were revoked")
		return nil
	})
}

// revertSpool resolves the snapshot spool: --revert-dir, then $CODEPLO_REVERT_DIR, then <data-dir>/revert
// (equal to hardening.DefaultRevertDir for the default data directory).
func revertSpool(cfg config.Config, flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("CODEPLO_REVERT_DIR"); v != "" {
		return v
	}
	return filepath.Join(cfg.DataDir, "revert")
}

// revert is the daemon-independent safety net: restore the snapshot files, replay the live-state steps recorded
// at apply time (the dedicated nftables table, an sshd reload), then open the database to move the change row
// to reverted and write the chained hardening.reverted audit row. Any failure returns an error, so the transient
// timer exits non-zero and the journal shows the problem.
func revert(ctx context.Context, cfg config.Config, spool string, list bool, args []string) error {
	if list {
		return revertList(ctx, cfg, spool)
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: codeplo revert <changeId|all> | codeplo revert --list")
	}
	ids, err := revertTargets(spool, args[0])
	if err != nil {
		return err
	}
	var failed []string
	for _, id := range ids {
		if err := revertOne(ctx, cfg, spool, id); err != nil {
			// keep going: one snapshot that will not restore must not leave the others armed against the operator
			failed = append(failed, id+": "+err.Error())
			fmt.Fprintf(os.Stderr, "revert failed  %s: %v\n", id, err)
			continue
		}
		fmt.Printf("reverted  %s (snapshot %s)\n", id, filepath.Join(spool, id))
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// revertTargets resolves the argument to snapshot ids. An id without a complete snapshot is refused: nothing is
// ever guessed at.
func revertTargets(spool, target string) ([]string, error) {
	if target != "all" {
		if !changeIDRe.MatchString(target) {
			return nil, fmt.Errorf("invalid change id %q", target)
		}
		if !hasSnapshot(spool, target) {
			return nil, fmt.Errorf("no snapshot for %q in %s: refusing to guess at its contents", target, spool)
		}
		return []string{target}, nil
	}
	ids := spoolSnapshots(spool)
	if len(ids) == 0 {
		return nil, fmt.Errorf("no snapshots with a %s under %s", hardening.SnapshotManifest, spool)
	}
	return ids, nil
}

func hasSnapshot(spool, id string) bool {
	_, err := os.Stat(filepath.Join(spool, id, hardening.SnapshotManifest))
	return err == nil
}

// spoolSnapshots lists every spool directory holding a complete snapshot manifest, newest name last.
func spoolSnapshots(spool string) []string {
	ents, err := os.ReadDir(spool)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() && changeIDRe.MatchString(e.Name()) && hasSnapshot(spool, e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

func revertOne(ctx context.Context, cfg config.Config, spool, id string) error {
	if err := hardening.Revert("/", spool, id); err != nil {
		return fmt.Errorf("restore files: %w", err)
	}
	if err := hardening.RuntimeRevert(ctx, spool, id, platform.DefaultRunner); err != nil {
		return fmt.Errorf("restore live state: %w", err)
	}
	return recordRevert(ctx, cfg, spool, id)
}

// recordRevert writes the state change and its audit row in one transaction. The files are already restored at
// this point, so a database failure is reported but never re-applies anything.
func recordRevert(ctx context.Context, cfg config.Config, spool, id string) error {
	db, _, err := app.Open(cfg)
	if err != nil {
		return fmt.Errorf("files restored; the database could not be opened to record the revert: %w", err)
	}
	defer db.Close()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	return store.InTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE hardening_changes SET state='reverted', reverted_at=? WHERE id=?`, now, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("snapshot %s was restored but has no hardening_changes row, so nothing was recorded", id)
		}
		_, err = audit.Append(ctx, tx, audit.Entry{
			ActorType:  "system",
			ActorID:    "cli",
			Action:     "hardening.reverted",
			TargetType: "hardening_change",
			TargetID:   id,
			Meta:       map[string]any{"source": "codeplo revert", "snapshot_path": filepath.Join(spool, id), "reverted_at": now},
		})
		return err
	})
}

// revertList shows what is still armed against the operator: open changes with their confirm deadlines, plus any
// snapshot on disk the database does not have a row for.
func revertList(ctx context.Context, cfg config.Config, spool string) error {
	db, _, err := app.Open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, check_key, tier, state, COALESCE(confirm_deadline,'') FROM hardening_changes
		WHERE state IN ('applied','pending_confirm','reverting') ORDER BY CASE WHEN confirm_deadline IS NULL THEN 1 ELSE 0 END, confirm_deadline, applied_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	recorded := map[string]bool{}
	for rows.Next() {
		var id, key, state, deadline string
		var tier int
		if err := rows.Scan(&id, &key, &tier, &state, &deadline); err != nil {
			return err
		}
		recorded[id] = true
		if deadline == "" {
			deadline = "none"
		}
		fmt.Printf("open       %s key=%s tier=%d state=%s confirm_deadline=%s snapshot=%s\n", id, key, tier, state, deadline, filepath.Join(spool, id))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range spoolSnapshots(spool) {
		if !recorded[id] {
			fmt.Printf("unrecorded %s snapshot=%s (restore with: codeplo revert %s)\n", id, filepath.Join(spool, id), id)
		}
	}
	return nil
}

// statusHardening reports the open Tier 2/3 exposure the operator can otherwise only see in the UI.
func statusHardening(ctx context.Context, db *sql.DB, spool string) error {
	var open int
	var deadline sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(confirm_deadline) FROM hardening_changes WHERE state IN ('applied','pending_confirm','reverting')`).Scan(&open, &deadline); err != nil {
		return err
	}
	var score sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT score FROM hardening_runs WHERE mode='audit' ORDER BY started_at DESC LIMIT 1`).Scan(&score); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	fmt.Printf("hardening open_changes=%d earliest_confirm_deadline=%s last_audit_score=%s snapshots=%d spool=%s\n",
		open, orNoneS(deadline), orNoneI(score), len(spoolSnapshots(spool)), spool)
	return nil
}

func orNoneS(s sql.NullString) string {
	if !s.Valid || s.String == "" {
		return "none"
	}
	return s.String
}

func orNoneI(n sql.NullInt64) string {
	if !n.Valid {
		return "none"
	}
	return strconv.FormatInt(n.Int64, 10)
}
