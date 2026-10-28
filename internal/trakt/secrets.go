package trakt

import (
	"context"
	"fmt"

	"github.com/braedonsaunders/ether/internal/config"
)

// Stored tokens are sealed per user and column (config.FieldSealer), so a
// token copied to another user's row does not open.

func tokenContext(userID int64, column string) []string {
	return []string{"trakt_tokens", column, config.FieldContext(userID)}
}

func (c *Client) sealTokens(userID int64, access, refresh string) (string, string, error) {
	a, err := config.SealWith(c.sealer, access, tokenContext(userID, "access_token")...)
	if err != nil {
		return "", "", fmt.Errorf("trakt: seal tokens: %w", err)
	}
	r, err := config.SealWith(c.sealer, refresh, tokenContext(userID, "refresh_token")...)
	if err != nil {
		return "", "", fmt.Errorf("trakt: seal tokens: %w", err)
	}
	return a, r, nil
}

// openTokens opens a stored token pair; legacy reports a plaintext token.
func (c *Client) openTokens(userID int64, access, refresh string) (string, string, bool, error) {
	a, la, err := config.OpenWith(c.sealer, access, tokenContext(userID, "access_token")...)
	if err != nil {
		return "", "", false, fmt.Errorf("trakt: stored tokens for user %d: %w", userID, err)
	}
	r, lr, err := config.OpenWith(c.sealer, refresh, tokenContext(userID, "refresh_token")...)
	if err != nil {
		return "", "", false, fmt.Errorf("trakt: stored tokens for user %d: %w", userID, err)
	}
	return a, r, la || lr, nil
}

// MigrateSecrets seals tokens stored in plaintext and returns how many
// users' rows it rewrote. Each row is replaced only if it still holds the
// values read (a concurrent refresh wins), so it is safe to run on every
// node and to rerun. A row that does not open is left untouched and
// reported in the error.
func (c *Client) MigrateSecrets(ctx context.Context) (int, error) {
	if c.db == nil || c.sealer == nil {
		return 0, nil
	}
	type row struct {
		user            int64
		access, refresh string
	}
	rows, err := c.db.QueryContext(ctx, `SELECT user_id, access_token, refresh_token FROM trakt_tokens ORDER BY user_id`)
	if err != nil {
		return 0, err
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.user, &r.access, &r.refresh); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n, failed := 0, 0
	var firstErr error
	for _, r := range all {
		access, refresh, legacy, err := c.openTokens(r.user, r.access, r.refresh)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !legacy {
			continue
		}
		sa, sr, err := c.sealTokens(r.user, access, refresh)
		if err != nil {
			return n, err
		}
		if sa == r.access && sr == r.refresh {
			continue // sealing not enabled yet
		}
		res, err := c.db.ExecContext(ctx, `UPDATE trakt_tokens SET access_token=?, refresh_token=? WHERE user_id=? AND access_token=? AND refresh_token=?`,
			sa, sr, r.user, r.access, r.refresh)
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	if failed > 0 {
		return n, fmt.Errorf("trakt: %d stored token row(s) could not be opened and were left unchanged: %w", failed, firstErr)
	}
	return n, nil
}
