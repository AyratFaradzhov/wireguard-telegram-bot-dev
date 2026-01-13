package access

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"

	"github.com/skoret/wireguard-bot/internal/storage"
)

type CheckResult struct {
	CanProvision bool
	Reason       string
}

type Service struct {
	repo *storage.Repository
}

func NewService(repo *storage.Repository) *Service {
	return &Service{
		repo: repo,
	}
}

// CanProvisionDevice checks if user can provision a new device
func (s *Service) CanProvisionDevice(ctx context.Context, userID int64) (*CheckResult, error) {
	// Get active subscription
	subscription, err := s.repo.GetActiveSubscriptionByUserID(ctx, userID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get subscription")
	}

	if subscription == nil {
		return &CheckResult{
			CanProvision: false,
			Reason:       "У вас нет активной подписки. Оформите оплату через меню бота.",
		}, nil
	}

	// Check subscription status
	now := time.Now()
	switch subscription.Status {
	case storage.SubscriptionStatusExpired:
		return &CheckResult{
			CanProvision: false,
			Reason:       "Ваша подписка истекла. Оформите продление через меню бота.",
		}, nil
	case storage.SubscriptionStatusPaused:
		// In grace period
		if subscription.GracePeriodEndsAt != nil && now.After(*subscription.GracePeriodEndsAt) {
			return &CheckResult{
				CanProvision: false,
				Reason:       "Ваша подписка истекла. Оформите продление через меню бота.",
			}, nil
		}
		return &CheckResult{
			CanProvision: false,
			Reason:       "Ваша подписка приостановлена. Оформите продление через меню бота.",
		}, nil
	}

	// Check if subscription has ended but not yet expired
	if now.After(subscription.EndsAt) {
		return &CheckResult{
			CanProvision: false,
			Reason:       "Ваша подписка истекла. Оформите продление через меню бота.",
		}, nil
	}

	// Check device limit
	deviceCount, err := s.repo.CountActiveDevicesBySubscription(ctx, subscription.ID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to count devices")
	}

	if deviceCount >= subscription.DeviceLimit {
		return &CheckResult{
			CanProvision: false,
			Reason: fmt.Sprintf("Достигнут лимит устройств (%d/%d). Отзовите одно из устройств или оформите продление с большим количеством устройств.",
				deviceCount, subscription.DeviceLimit),
		}, nil
	}

	return &CheckResult{
		CanProvision: true,
		Reason:       "",
	}, nil
}



