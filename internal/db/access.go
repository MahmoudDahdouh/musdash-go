package db

import (
	"context"
	"database/sql"
)

// SetTOTPPending stores a key that was shown to the person and waits for
// a code to confirm it. It replaces one that was never confirmed.
func (d *DB) SetTOTPPending(ctx context.Context, userID, sealedKey string) error {
	return affected(d.ExecContext(ctx, `UPDATE users SET totp_pending = ? WHERE id = ?`, sealedKey, userID))
}

// EnableTOTP makes the pending key the person's second step. step is the
// step of the code that confirmed it, so that code cannot be used again
// to sign in. Every other session ends, and the recovery codes are
// replaced by the given ones.
func (d *DB) EnableTOTP(ctx context.Context, userID, sealedKey string, step int64, keepTokenHash string, codeHashes []string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := affected(tx.ExecContext(ctx, `UPDATE users SET totp_secret = ?, totp_pending = '', totp_step = ? WHERE id = ? AND totp_pending = ?`,
			sealedKey, step, userID, sealedKey)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keepTokenHash); err != nil {
			return err
		}
		return replaceRecoveryCodes(ctx, tx, userID, codeHashes)
	})
}

// DisableTOTP removes a person's second step and its recovery codes.
func (d *DB) DisableTOTP(ctx context.Context, userID string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if err := affected(tx.ExecContext(ctx, `UPDATE users SET totp_secret = '', totp_pending = '', totp_step = 0 WHERE id = ?`, userID)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
		return err
	})
}

// UseTOTPStep records that a code of this step was accepted. It returns
// ErrNotFound when a code of this step or a later one was accepted
// before: the check and the write are one statement, so two requests with
// the same code cannot both pass.
func (d *DB) UseTOTPStep(ctx context.Context, userID string, step int64) error {
	return affected(d.ExecContext(ctx, `UPDATE users SET totp_step = ? WHERE id = ? AND totp_secret <> '' AND totp_step < ?`, step, userID, step))
}

// UseRecoveryCode uses a recovery code up. It returns ErrNotFound for a
// code that is not the person's or was used.
func (d *DB) UseRecoveryCode(ctx context.Context, userID, codeHash string) error {
	return affected(d.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ? AND code_hash = ?`, userID, codeHash))
}

// ReplaceRecoveryCodes gives a person a new set of recovery codes.
func (d *DB) ReplaceRecoveryCodes(ctx context.Context, userID string, codeHashes []string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error { return replaceRecoveryCodes(ctx, tx, userID, codeHashes) })
}

func replaceRecoveryCodes(ctx context.Context, tx *sql.Tx, userID string, codeHashes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range codeHashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return err
		}
	}
	return nil
}

// CountRecoveryCodes reports how many recovery codes a person has left.
func (d *DB) CountRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}
