package storage

import (
	"context"
	"fmt"
)

// Migrate creates all necessary tables
func (r *Repository) Migrate(ctx context.Context) error {
	migrations := []struct {
		name string
		sql  string
	}{
		{
			name: "create_users",
			sql: `CREATE TABLE IF NOT EXISTS users (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				telegram_id INTEGER NOT NULL UNIQUE,
				username TEXT NOT NULL,
				created_at DATETIME NOT NULL
			)`,
		},
		{
			name: "create_payments",
			sql: `CREATE TABLE IF NOT EXISTS payments (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				duration_days INTEGER NOT NULL,
				device_count INTEGER NOT NULL,
				amount INTEGER NOT NULL,
				reference_code TEXT NOT NULL UNIQUE,
				payment_comment TEXT NOT NULL UNIQUE,
				status TEXT NOT NULL,
				proof_file_id TEXT,
				created_at DATETIME NOT NULL,
				reviewed_at DATETIME,
				reviewed_by TEXT,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			)`,
		},
		{
			name: "create_subscriptions",
			sql: `CREATE TABLE IF NOT EXISTS subscriptions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				duration_days INTEGER NOT NULL,
				device_limit INTEGER NOT NULL,
				amount INTEGER NOT NULL,
				status TEXT NOT NULL,
				starts_at DATETIME NOT NULL,
				ends_at DATETIME NOT NULL,
				grace_period_ends_at DATETIME,
				created_at DATETIME NOT NULL,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
			)`,
		},
		{
			name: "create_devices",
			sql: `CREATE TABLE IF NOT EXISTS devices (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				subscription_id INTEGER NOT NULL,
				device_name TEXT NOT NULL,
				peer_public_key TEXT NOT NULL UNIQUE,
				assigned_ip TEXT NOT NULL,
				created_at DATETIME NOT NULL,
				revoked_at DATETIME,
				FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
				FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) ON DELETE CASCADE
			)`,
		},
		{
			name: "create_indexes",
			sql: `CREATE INDEX IF NOT EXISTS idx_payments_user_id ON payments(user_id);
				CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
				CREATE INDEX IF NOT EXISTS idx_payments_reference_code ON payments(reference_code);
				CREATE INDEX IF NOT EXISTS idx_payments_comment ON payments(payment_comment);
				CREATE INDEX IF NOT EXISTS idx_subscriptions_user_id ON subscriptions(user_id);
				CREATE INDEX IF NOT EXISTS idx_subscriptions_status ON subscriptions(status);
				CREATE INDEX IF NOT EXISTS idx_devices_user_id ON devices(user_id);
				CREATE INDEX IF NOT EXISTS idx_devices_subscription_id ON devices(subscription_id);
				CREATE INDEX IF NOT EXISTS idx_devices_peer_public_key ON devices(peer_public_key);
			`,
		},
	}

	for _, migration := range migrations {
		if _, err := r.db.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("migration %s failed: %w", migration.name, err)
		}
	}

	// Add payment_comment column if it doesn't exist (for existing databases)
	// SQLite doesn't support IF NOT EXISTS for ALTER TABLE ADD COLUMN
	// We'll try to add it and ignore the error if it already exists
	_, _ = r.db.ExecContext(ctx, `ALTER TABLE payments ADD COLUMN payment_comment TEXT;`)
	// Create unique index (will be ignored if already exists)
	_, _ = r.db.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_comment ON payments(payment_comment) WHERE payment_comment IS NOT NULL;
	`)

	return nil
}

