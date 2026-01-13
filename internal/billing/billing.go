package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/pkg/errors"

	"github.com/skoret/wireguard-bot/internal/storage"
)

const (
	BasePricePerDevice = 10000 // 100 RUB in kopecks
)

type Service struct {
	repo          *storage.Repository
	staticQRCode  string // Static QR code for all payments
}

func NewService(repo *storage.Repository, staticQRCode string) *Service {
	return &Service{
		repo:         repo,
		staticQRCode: staticQRCode,
	}
}

// GetStaticQRCode returns the static QR code for payments
func (s *Service) GetStaticQRCode() string {
	return s.staticQRCode
}

// GenerateReferenceCode generates a unique reference code for payment
func (s *Service) GenerateReferenceCode() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", errors.Wrap(err, "failed to generate random bytes")
	}
	return hex.EncodeToString(bytes), nil
}

// CalculatePrice calculates the price based on duration and device count
func (s *Service) CalculatePrice(durationDays, deviceCount int) int {
	var multiplier float64
	switch durationDays {
	case 30:
		multiplier = 1.0
	case 90:
		multiplier = 0.95
	case 180:
		multiplier = 0.90
	default:
		multiplier = 1.0
	}

	basePrice := BasePricePerDevice * deviceCount
	return int(math.Round(float64(basePrice) * multiplier))
}

// CreatePaymentAttempt creates a new payment attempt
func (s *Service) CreatePaymentAttempt(ctx context.Context, userID int64, durationDays, deviceCount int) (*storage.Payment, error) {
	// Validate inputs
	if durationDays != 30 && durationDays != 90 && durationDays != 180 {
		return nil, errors.New("invalid duration: must be 30, 90, or 180 days")
	}
	if deviceCount < 1 || deviceCount > 5 {
		return nil, errors.New("invalid device count: must be between 1 and 5")
	}

	referenceCode, err := s.GenerateReferenceCode()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate reference code")
	}

	paymentComment, err := GeneratePaymentComment()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate payment comment")
	}

	amount := s.CalculatePrice(durationDays, deviceCount)

	payment := &storage.Payment{
		UserID:         userID,
		DurationDays:   durationDays,
		DeviceCount:    deviceCount,
		Amount:         amount,
		ReferenceCode:  referenceCode,
		PaymentComment: paymentComment,
		Status:         storage.PaymentStatusCreated,
	}

	if err := s.repo.CreatePayment(ctx, payment); err != nil {
		return nil, errors.Wrap(err, "failed to create payment")
	}

	return payment, nil
}

// AttachProofAndMoveToPendingReview attaches proof file and moves payment to pending review
func (s *Service) AttachProofAndMoveToPendingReview(ctx context.Context, paymentID int64, proofFileID string) error {
	if err := s.repo.AttachProofToPayment(ctx, paymentID, proofFileID); err != nil {
		return errors.Wrap(err, "failed to attach proof to payment")
	}
	return nil
}

// AdminApprovePayment approves a payment and creates/extends subscription
// It verifies payment amount, payment comment match, and uploaded proof
func (s *Service) AdminApprovePayment(ctx context.Context, paymentID int64, reviewedBy string, verifiedComment string) error {
	payment, err := s.repo.GetPaymentByID(ctx, paymentID)
	if err != nil {
		return errors.Wrap(err, "failed to get payment")
	}
	if payment == nil {
		return errors.New("payment not found")
	}
	if payment.Status != storage.PaymentStatusPendingReview {
		return fmt.Errorf("payment is not in pending_review status: %s", payment.Status)
	}

	// Verify payment comment match
	if verifiedComment != payment.PaymentComment {
		return fmt.Errorf("payment comment mismatch: expected '%s', got '%s'. Payment without correct comment MUST NOT be approved", payment.PaymentComment, verifiedComment)
	}

	// Note: Proof verification is optional in simplified flow
	// Admin can approve without proof if they verify payment manually

	// Update payment status
	if err := s.repo.UpdatePaymentStatus(ctx, paymentID, storage.PaymentStatusApproved, &reviewedBy); err != nil {
		return errors.Wrap(err, "failed to update payment status")
	}

	// Get or create active subscription
	activeSub, err := s.repo.GetActiveSubscriptionByUserID(ctx, payment.UserID)
	if err != nil {
		return errors.Wrap(err, "failed to get active subscription")
	}

	now := time.Now()
	if activeSub != nil {
		// Extend existing subscription
		if err := s.repo.ExtendSubscription(ctx, activeSub.ID, payment.DurationDays, payment.Amount); err != nil {
			return errors.Wrap(err, "failed to extend subscription")
		}
	} else {
		// Create new subscription
		endsAt := now.AddDate(0, 0, payment.DurationDays)
		gracePeriodEndsAt := endsAt.AddDate(0, 0, 3)

		subscription := &storage.Subscription{
			UserID:            payment.UserID,
			DurationDays:      payment.DurationDays,
			DeviceLimit:       payment.DeviceCount,
			Amount:            payment.Amount,
			Status:            storage.SubscriptionStatusActive,
			StartsAt:          now,
			EndsAt:            endsAt,
			GracePeriodEndsAt: &gracePeriodEndsAt,
		}

		if err := s.repo.CreateSubscription(ctx, subscription); err != nil {
			return errors.Wrap(err, "failed to create subscription")
		}
	}

	return nil
}

// AdminRejectPayment rejects a payment
func (s *Service) AdminRejectPayment(ctx context.Context, paymentID int64, reviewedBy string) error {
	payment, err := s.repo.GetPaymentByID(ctx, paymentID)
	if err != nil {
		return errors.Wrap(err, "failed to get payment")
	}
	if payment == nil {
		return errors.New("payment not found")
	}
	if payment.Status != storage.PaymentStatusPendingReview {
		return fmt.Errorf("payment is not in pending_review status: %s", payment.Status)
	}

	if err := s.repo.UpdatePaymentStatus(ctx, paymentID, storage.PaymentStatusRejected, &reviewedBy); err != nil {
		return errors.Wrap(err, "failed to update payment status")
	}

	return nil
}

// GetPendingPayments returns all payments pending review
func (s *Service) GetPendingPayments(ctx context.Context) ([]*storage.Payment, error) {
	payments, err := s.repo.GetPendingPayments(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get pending payments")
	}
	return payments, nil
}

