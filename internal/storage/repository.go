package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Repository struct {
	db *sql.DB
}

// NewRepository creates a new repository instance
func NewRepository(dsn string) (*Repository, error) {
	log.Printf("Initializing repository with DSN: %s", dsn)
	
	// Handle file: prefix (remove it)
	if strings.HasPrefix(dsn, "file:") {
		dsn = strings.TrimPrefix(dsn, "file:")
		log.Printf("Removed 'file:' prefix, using DSN: %s", dsn)
	}
	
	// For SQLite in dev, PostgreSQL in production
	// Check if dsn contains postgres:// pattern
	var driver string
	if len(dsn) > 10 && strings.HasPrefix(dsn, "postgres://") {
		driver = "postgres"
		return nil, fmt.Errorf("PostgreSQL not yet implemented, use SQLite for now (DSN: %s)", dsn)
	} else {
		// Default to SQLite
		driver = "sqlite"
		if dsn == "" {
			dsn = "bot.db"
			log.Printf("DSN is empty, using default: %s", dsn)
		}
	}

	log.Printf("Using driver: %s, DSN: %s", driver, dsn)

	var db *sql.DB
	var err error
	if driver == "sqlite" {
		// For file-based SQLite, ensure directory exists and is writable
		if dsn != ":memory:" {
			// Resolve absolute path
			absPath, err := filepath.Abs(dsn)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve database path '%s': %w", dsn, err)
			}
			log.Printf("Resolved database path to: %s", absPath)

			// Get directory
			dbDir := filepath.Dir(absPath)
			log.Printf("Database directory: %s", dbDir)

			// Create directory if it doesn't exist
			if err := os.MkdirAll(dbDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create database directory '%s': %w", dbDir, err)
			}

			// Test write permission
			testFile := filepath.Join(dbDir, ".write_test")
			f, err := os.Create(testFile)
			if err != nil {
				return nil, fmt.Errorf("database directory '%s' is not writable: %w", dbDir, err)
			}
			f.Close()
			os.Remove(testFile)
			log.Printf("Database directory is writable")
		}

		db, err = sql.Open("sqlite", dsn+"?_foreign_keys=1")
		if err != nil {
			return nil, fmt.Errorf("failed to open SQLite database '%s': %w", dsn, err)
		}
		log.Printf("SQLite database opened successfully")
	} else {
		return nil, fmt.Errorf("unsupported driver '%s', only SQLite is supported (DSN: %s)", driver, dsn)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database '%s': %w", dsn, err)
	}
	log.Printf("Database connection established successfully")

	return &Repository{db: db}, nil
}

func (r *Repository) Close() error {
	return r.db.Close()
}

// User operations

func (r *Repository) GetOrCreateUser(ctx context.Context, telegramID int64, username string) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		"SELECT id, telegram_id, username, created_at FROM users WHERE telegram_id = ?",
		telegramID,
	).Scan(&user.ID, &user.TelegramID, &user.Username, &user.CreatedAt)

	if err == nil {
		return user, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	// User doesn't exist, create it
	result, err := r.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, username, created_at) VALUES (?, ?, ?)",
		telegramID, username, time.Now(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get last insert id: %w", err)
	}

	return &User{
		ID:         id,
		TelegramID: telegramID,
		Username:   username,
		CreatedAt:  time.Now(),
	}, nil
}

func (r *Repository) GetUserByTelegramID(ctx context.Context, telegramID int64) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		"SELECT id, telegram_id, username, created_at FROM users WHERE telegram_id = ?",
		telegramID,
	).Scan(&user.ID, &user.TelegramID, &user.Username, &user.CreatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	return user, nil
}

func (r *Repository) GetUserByID(ctx context.Context, id int64) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		"SELECT id, telegram_id, username, created_at FROM users WHERE id = ?",
		id,
	).Scan(&user.ID, &user.TelegramID, &user.Username, &user.CreatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	return user, nil
}

func (r *Repository) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		"SELECT id, telegram_id, username, created_at FROM users WHERE username = ?",
		username,
	).Scan(&user.ID, &user.TelegramID, &user.Username, &user.CreatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query user by username: %w", err)
	}

	return user, nil
}

// Payment operations

func (r *Repository) CreatePayment(ctx context.Context, payment *Payment) error {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO payments (user_id, duration_days, device_count, amount, reference_code, payment_comment, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		payment.UserID, payment.DurationDays, payment.DeviceCount, payment.Amount,
		payment.ReferenceCode, payment.PaymentComment, payment.Status, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to create payment: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	payment.ID = id
	return nil
}

func (r *Repository) GetPaymentByID(ctx context.Context, id int64) (*Payment, error) {
	payment := &Payment{}
	var proofFileID sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, duration_days, device_count, amount, reference_code, payment_comment, status,
		 proof_file_id, created_at, reviewed_at, reviewed_by
		 FROM payments WHERE id = ?`,
		id,
	).Scan(
		&payment.ID, &payment.UserID, &payment.DurationDays, &payment.DeviceCount,
		&payment.Amount, &payment.ReferenceCode, &payment.PaymentComment, &payment.Status,
		&proofFileID, &payment.CreatedAt, &payment.ReviewedAt, &payment.ReviewedBy,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query payment: %w", err)
	}
	if proofFileID.Valid {
		payment.ProofFileID = proofFileID.String
	}
	return payment, nil
}

func (r *Repository) GetPaymentByReferenceCode(ctx context.Context, referenceCode string) (*Payment, error) {
	payment := &Payment{}
	var proofFileID sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, duration_days, device_count, amount, reference_code, payment_comment, status,
		 proof_file_id, created_at, reviewed_at, reviewed_by
		 FROM payments WHERE reference_code = ?`,
		referenceCode,
	).Scan(
		&payment.ID, &payment.UserID, &payment.DurationDays, &payment.DeviceCount,
		&payment.Amount, &payment.ReferenceCode, &payment.PaymentComment, &payment.Status,
		&proofFileID, &payment.CreatedAt, &payment.ReviewedAt, &payment.ReviewedBy,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query payment: %w", err)
	}
	if proofFileID.Valid {
		payment.ProofFileID = proofFileID.String
	}
	return payment, nil
}

func (r *Repository) GetPaymentsByUserIDAndStatus(ctx context.Context, userID int64, status PaymentStatus) ([]*Payment, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, duration_days, device_count, amount, reference_code, payment_comment, status,
		 proof_file_id, created_at, reviewed_at, reviewed_by
		 FROM payments WHERE user_id = ? AND status = ? ORDER BY created_at ASC`,
		userID, status,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query payments: %w", err)
	}
	defer rows.Close()

	var payments []*Payment
	for rows.Next() {
		payment := &Payment{}
		var proofFileID sql.NullString
		err := rows.Scan(
			&payment.ID, &payment.UserID, &payment.DurationDays, &payment.DeviceCount,
			&payment.Amount, &payment.ReferenceCode, &payment.PaymentComment, &payment.Status,
			&proofFileID, &payment.CreatedAt, &payment.ReviewedAt, &payment.ReviewedBy,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		if proofFileID.Valid {
			payment.ProofFileID = proofFileID.String
		}
		payments = append(payments, payment)
	}
	return payments, nil
}

func (r *Repository) GetPendingPayments(ctx context.Context) ([]*Payment, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, duration_days, device_count, amount, reference_code, payment_comment, status,
		 proof_file_id, created_at, reviewed_at, reviewed_by
		 FROM payments WHERE status = ? ORDER BY created_at ASC`,
		PaymentStatusPendingReview,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending payments: %w", err)
	}
	defer rows.Close()

	var payments []*Payment
	for rows.Next() {
		payment := &Payment{}
		var proofFileID sql.NullString
		err := rows.Scan(
			&payment.ID, &payment.UserID, &payment.DurationDays, &payment.DeviceCount,
			&payment.Amount, &payment.ReferenceCode, &payment.PaymentComment, &payment.Status,
			&proofFileID, &payment.CreatedAt, &payment.ReviewedAt, &payment.ReviewedBy,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan payment: %w", err)
		}
		if proofFileID.Valid {
			payment.ProofFileID = proofFileID.String
		}
		payments = append(payments, payment)
	}
	return payments, nil
}

func (r *Repository) UpdatePaymentStatus(ctx context.Context, id int64, status PaymentStatus, reviewedBy *string) error {
	now := time.Now()
	_, err := r.db.ExecContext(ctx,
		`UPDATE payments SET status = ?, reviewed_at = ?, reviewed_by = ? WHERE id = ?`,
		status, now, reviewedBy, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update payment status: %w", err)
	}
	return nil
}

func (r *Repository) AttachProofToPayment(ctx context.Context, id int64, proofFileID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE payments SET status = ?, proof_file_id = ? WHERE id = ?`,
		PaymentStatusPendingReview, proofFileID, id,
	)
	if err != nil {
		return fmt.Errorf("failed to attach proof to payment: %w", err)
	}
	return nil
}

// Subscription operations

func (r *Repository) CreateSubscription(ctx context.Context, subscription *Subscription) error {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO subscriptions (user_id, duration_days, device_limit, amount, status, starts_at, ends_at, grace_period_ends_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		subscription.UserID, subscription.DurationDays, subscription.DeviceLimit, subscription.Amount,
		subscription.Status, subscription.StartsAt, subscription.EndsAt, subscription.GracePeriodEndsAt, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to create subscription: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	subscription.ID = id
	return nil
}

func (r *Repository) GetActiveSubscriptionByUserID(ctx context.Context, userID int64) (*Subscription, error) {
	subscription := &Subscription{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, duration_days, device_limit, amount, status, starts_at, ends_at, grace_period_ends_at, created_at
		 FROM subscriptions WHERE user_id = ? AND status IN (?, ?, ?) ORDER BY created_at DESC LIMIT 1`,
		userID, SubscriptionStatusActive, SubscriptionStatusExpiring, SubscriptionStatusPaused,
	).Scan(
		&subscription.ID, &subscription.UserID, &subscription.DurationDays, &subscription.DeviceLimit,
		&subscription.Amount, &subscription.Status, &subscription.StartsAt, &subscription.EndsAt,
		&subscription.GracePeriodEndsAt, &subscription.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query subscription: %w", err)
	}
	return subscription, nil
}

func (r *Repository) UpdateSubscriptionStatus(ctx context.Context, id int64, status SubscriptionStatus) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE subscriptions SET status = ? WHERE id = ?`,
		status, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update subscription status: %w", err)
	}
	return nil
}

func (r *Repository) GetSubscriptionsNeedingUpdate(ctx context.Context, now time.Time) ([]*Subscription, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, duration_days, device_limit, amount, status, starts_at, ends_at, grace_period_ends_at, created_at
		 FROM subscriptions WHERE status IN (?, ?, ?)`,
		SubscriptionStatusActive, SubscriptionStatusExpiring, SubscriptionStatusPaused,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query subscriptions: %w", err)
	}
	defer rows.Close()

	var subscriptions []*Subscription
	for rows.Next() {
		sub := &Subscription{}
		err := rows.Scan(
			&sub.ID, &sub.UserID, &sub.DurationDays, &sub.DeviceLimit,
			&sub.Amount, &sub.Status, &sub.StartsAt, &sub.EndsAt,
			&sub.GracePeriodEndsAt, &sub.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan subscription: %w", err)
		}
		subscriptions = append(subscriptions, sub)
	}
	return subscriptions, nil
}

func (r *Repository) ExtendSubscription(ctx context.Context, subscriptionID int64, durationDays int, amount int) error {
	// Get current subscription
	sub, err := r.GetSubscriptionByID(ctx, subscriptionID)
	if err != nil {
		return err
	}
	if sub == nil {
		return errors.New("subscription not found")
	}

	// Calculate new end date from current end date
	newEndsAt := sub.EndsAt.AddDate(0, 0, durationDays)
	gracePeriodEndsAt := newEndsAt.AddDate(0, 0, 3)

	_, err = r.db.ExecContext(ctx,
		`UPDATE subscriptions SET duration_days = duration_days + ?, amount = amount + ?, ends_at = ?, grace_period_ends_at = ?, status = ? WHERE id = ?`,
		durationDays, amount, newEndsAt, gracePeriodEndsAt, SubscriptionStatusActive, subscriptionID,
	)
	if err != nil {
		return fmt.Errorf("failed to extend subscription: %w", err)
	}
	return nil
}

func (r *Repository) GetSubscriptionByID(ctx context.Context, id int64) (*Subscription, error) {
	subscription := &Subscription{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, duration_days, device_limit, amount, status, starts_at, ends_at, grace_period_ends_at, created_at
		 FROM subscriptions WHERE id = ?`,
		id,
	).Scan(
		&subscription.ID, &subscription.UserID, &subscription.DurationDays, &subscription.DeviceLimit,
		&subscription.Amount, &subscription.Status, &subscription.StartsAt, &subscription.EndsAt,
		&subscription.GracePeriodEndsAt, &subscription.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query subscription: %w", err)
	}
	return subscription, nil
}

// Device operations

func (r *Repository) CreateDevice(ctx context.Context, device *Device) error {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO devices (user_id, subscription_id, device_name, peer_public_key, assigned_ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		device.UserID, device.SubscriptionID, device.DeviceName, device.PeerPublicKey,
		device.AssignedIP, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to create device: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	device.ID = id
	return nil
}

func (r *Repository) GetDeviceByPeerPublicKey(ctx context.Context, peerPublicKey string) (*Device, error) {
	device := &Device{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, subscription_id, device_name, peer_public_key, assigned_ip, created_at, revoked_at
		 FROM devices WHERE peer_public_key = ?`,
		peerPublicKey,
	).Scan(
		&device.ID, &device.UserID, &device.SubscriptionID, &device.DeviceName,
		&device.PeerPublicKey, &device.AssignedIP, &device.CreatedAt, &device.RevokedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query device: %w", err)
	}
	return device, nil
}

func (r *Repository) CountActiveDevicesBySubscription(ctx context.Context, subscriptionID int64) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM devices WHERE subscription_id = ? AND revoked_at IS NULL`,
		subscriptionID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count devices: %w", err)
	}
	return count, nil
}

func (r *Repository) RevokeDevice(ctx context.Context, deviceID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE devices SET revoked_at = ? WHERE id = ?`,
		time.Now(), deviceID,
	)
	if err != nil {
		return fmt.Errorf("failed to revoke device: %w", err)
	}
	return nil
}

func (r *Repository) GetExpiredDevicesToCleanup(ctx context.Context, before time.Time) ([]*Device, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT d.id, d.user_id, d.subscription_id, d.device_name, d.peer_public_key, d.assigned_ip, d.created_at, d.revoked_at
		 FROM devices d
		 JOIN subscriptions s ON d.subscription_id = s.id
		 WHERE s.status = ? AND s.grace_period_ends_at < ? AND d.revoked_at IS NULL`,
		SubscriptionStatusExpired, before,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query expired devices: %w", err)
	}
	defer rows.Close()

	var devices []*Device
	for rows.Next() {
		device := &Device{}
		err := rows.Scan(
			&device.ID, &device.UserID, &device.SubscriptionID, &device.DeviceName,
			&device.PeerPublicKey, &device.AssignedIP, &device.CreatedAt, &device.RevokedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan device: %w", err)
		}
		devices = append(devices, device)
	}
	return devices, nil
}

// Transaction operations

func (r *Repository) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return r.db.BeginTx(ctx, nil)
}
