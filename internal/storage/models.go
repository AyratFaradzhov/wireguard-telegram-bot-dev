package storage

import (
	"time"
)

// User represents a Telegram user
type User struct {
	ID         int64
	TelegramID int64
	Username   string
	CreatedAt  time.Time
}

// PaymentStatus represents payment status
type PaymentStatus string

const (
	PaymentStatusCreated       PaymentStatus = "created"
	PaymentStatusPendingReview PaymentStatus = "pending_review"
	PaymentStatusApproved      PaymentStatus = "approved"
	PaymentStatusRejected      PaymentStatus = "rejected"
	PaymentStatusExpired       PaymentStatus = "expired"
	PaymentStatusCancelled     PaymentStatus = "cancelled"
)

// Payment represents a payment attempt
type Payment struct {
	ID            int64
	UserID        int64
	DurationDays  int
	DeviceCount   int
	Amount        int // in kopecks (1 RUB = 100 kopecks)
	ReferenceCode string
	PaymentComment string // Unique neutral comment for payment (2-3 Russian words + suffix)
	Status        PaymentStatus
	ProofFileID   string
	CreatedAt     time.Time
	ReviewedAt    *time.Time
	ReviewedBy    *string
}

// SubscriptionStatus represents subscription status
type SubscriptionStatus string

const (
	SubscriptionStatusActive   SubscriptionStatus = "active"
	SubscriptionStatusExpiring SubscriptionStatus = "expiring"
	SubscriptionStatusPaused   SubscriptionStatus = "paused"
	SubscriptionStatusExpired  SubscriptionStatus = "expired"
)

// Subscription represents a user subscription
type Subscription struct {
	ID                int64
	UserID            int64
	DurationDays      int
	DeviceLimit       int
	Amount            int // in kopecks
	Status            SubscriptionStatus
	StartsAt          time.Time
	EndsAt            time.Time
	GracePeriodEndsAt *time.Time
	CreatedAt         time.Time
}

// Device represents a user device with WireGuard peer
type Device struct {
	ID            int64
	UserID        int64
	SubscriptionID int64
	DeviceName    string
	PeerPublicKey string
	AssignedIP    string
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

// GetTime returns current time (helper for testing)
func GetTime() time.Time {
	return time.Now()
}

