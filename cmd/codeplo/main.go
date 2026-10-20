// codeplo: break-glass CLI. Operates directly on the data directory (run as the service user).
package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/authn"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: codeplo doctor [--verify-audit] | reset-password --email E | disable-2fa --email E | status | setup-token")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, key, err := app.Open(cfg)
	_ = key
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	email := fs.String("email", "", "account email")
	verify := fs.Bool("verify-audit", false, "verify the audit hash chain")
	_ = fs.Parse(args)

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
	case "status":
		var users, sess, rows int
		var step string
		_ = db.QueryRow(`SELECT (SELECT COUNT(*) FROM users),(SELECT COUNT(*) FROM sessions WHERE pending_challenge IS NULL),(SELECT COUNT(*) FROM audit_events),(SELECT step FROM setup_state WHERE id=1)`).Scan(&users, &sess, &rows, &step)
		fmt.Printf("version=%s setup_step=%s users=%d sessions=%d audit_rows=%d\n", app.Version, step, users, sess, rows)
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
