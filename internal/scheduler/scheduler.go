package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/pkg/errors"

	"github.com/skoret/wireguard-bot/internal/storage"
	"github.com/skoret/wireguard-bot/internal/telegram"
)

type Service struct {
	repo    *storage.Repository
	bot     *telegram.Bot
	ctx     context.Context
	stop    chan struct{}
	running bool
}

func NewService(repo *storage.Repository, bot *telegram.Bot) *Service {
	return &Service{
		repo: repo,
		bot:  bot,
		stop: make(chan struct{}),
	}
}

// Start starts the scheduler
func (s *Service) Start(ctx context.Context) {
	s.ctx = ctx
	s.running = true

	// Run immediately on start
	go s.run()

	// Then run daily at midnight
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			go s.run()
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the scheduler
func (s *Service) Stop() {
	s.running = false
	close(s.stop)
}

func (s *Service) run() {
	if !s.running {
		return
	}

	log.Println("Running scheduler tasks...")
	now := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Update subscription statuses
	if err := s.updateSubscriptionStatuses(ctx, now); err != nil {
		log.Printf("Error updating subscription statuses: %v", err)
	}

	// Send notifications
	if err := s.sendNotifications(ctx, now); err != nil {
		log.Printf("Error sending notifications: %v", err)
	}

	// Revoke expired devices
	if err := s.revokeExpiredDevices(ctx, now); err != nil {
		log.Printf("Error revoking expired devices: %v", err)
	}

	log.Println("Scheduler tasks completed")
}

func (s *Service) updateSubscriptionStatuses(ctx context.Context, now time.Time) error {
	subscriptions, err := s.repo.GetSubscriptionsNeedingUpdate(ctx, now)
	if err != nil {
		return errors.Wrap(err, "failed to get subscriptions")
	}

	for _, sub := range subscriptions {
		var newStatus storage.SubscriptionStatus

		// Check if subscription is expiring (3 days before end)
		expiringThreshold := sub.EndsAt.AddDate(0, 0, -3)
		if now.After(expiringThreshold) && now.Before(sub.EndsAt) && sub.Status == storage.SubscriptionStatusActive {
			newStatus = storage.SubscriptionStatusExpiring
		} else if now.After(sub.EndsAt) && sub.Status == storage.SubscriptionStatusExpiring {
			// Move to paused (grace period)
			newStatus = storage.SubscriptionStatusPaused
		} else if sub.GracePeriodEndsAt != nil && now.After(*sub.GracePeriodEndsAt) && sub.Status == storage.SubscriptionStatusPaused {
			// Move to expired
			newStatus = storage.SubscriptionStatusExpired
		} else {
			continue // No status change needed
		}

		if err := s.repo.UpdateSubscriptionStatus(ctx, sub.ID, newStatus); err != nil {
			log.Printf("Failed to update subscription %d status: %v", sub.ID, err)
			continue
		}

		log.Printf("Updated subscription %d status to %s", sub.ID, newStatus)
	}

	return nil
}

func (s *Service) sendNotifications(ctx context.Context, now time.Time) error {
	subscriptions, err := s.repo.GetSubscriptionsNeedingUpdate(ctx, now)
	if err != nil {
		return errors.Wrap(err, "failed to get subscriptions")
	}

	for _, sub := range subscriptions {
		// Send notification 3 days before expiration
		expiringThreshold := sub.EndsAt.AddDate(0, 0, -3)
		expiringDayStart := time.Date(expiringThreshold.Year(), expiringThreshold.Month(), expiringThreshold.Day(), 0, 0, 0, 0, expiringThreshold.Location())
		expiringDayEnd := expiringDayStart.Add(24 * time.Hour)

		if sub.Status == storage.SubscriptionStatusActive && now.After(expiringDayStart) && now.Before(expiringDayEnd) {
			// Send expiring notification
			user, err := s.repo.GetUserByID(ctx, sub.UserID)
			if err != nil || user == nil {
				log.Printf("Failed to get user %d for notification: %v", sub.UserID, err)
				continue
			}

			daysLeft := int(sub.EndsAt.Sub(now).Hours() / 24)
			message := fmt.Sprintf(
				"⏰ Ваша подписка истекает через %d дн. (%s).\n\n"+
					"Оформите продление через меню бота, чтобы продолжить использование VPN.",
				daysLeft, sub.EndsAt.Format("02.01.2006"),
			)

			if err := s.bot.SendNotification(user.TelegramID, message); err != nil {
				log.Printf("Failed to send notification to user %d: %v", user.TelegramID, err)
			}
		}

		// Send notification when subscription ends
		if sub.Status == storage.SubscriptionStatusPaused && sub.GracePeriodEndsAt != nil {
			graceDayStart := time.Date(sub.GracePeriodEndsAt.Year(), sub.GracePeriodEndsAt.Month(), sub.GracePeriodEndsAt.Day(), 0, 0, 0, 0, sub.GracePeriodEndsAt.Location())
			graceDayEnd := graceDayStart.Add(24 * time.Hour)

			if now.After(graceDayStart) && now.Before(graceDayEnd) {
				user, err := s.repo.GetUserByID(ctx, sub.UserID)
				if err != nil || user == nil {
					log.Printf("Failed to get user %d for notification: %v", sub.UserID, err)
					continue
				}

				message := fmt.Sprintf(
					"⚠️ Ваша подписка истекла. У вас есть 3 дня (до %s) для продления, после чего устройства будут отключены.",
					sub.GracePeriodEndsAt.Format("02.01.2006"),
				)

				if err := s.bot.SendNotification(user.TelegramID, message); err != nil {
					log.Printf("Failed to send notification to user %d: %v", user.TelegramID, err)
				}
			}
		}
	}

	return nil
}

func (s *Service) revokeExpiredDevices(ctx context.Context, now time.Time) error {
	// Get devices that need to be revoked (30 days after grace period ends)
	cleanupDate := now.AddDate(0, 0, -30)
	devices, err := s.repo.GetExpiredDevicesToCleanup(ctx, cleanupDate)
	if err != nil {
		return errors.Wrap(err, "failed to get expired devices")
	}

	for _, device := range devices {
		if err := s.repo.RevokeDevice(ctx, device.ID); err != nil {
			log.Printf("Failed to revoke device %d: %v", device.ID, err)
			continue
		}

		log.Printf("Revoked expired device %d (user %d)", device.ID, device.UserID)
		// Note: Actual peer revocation from WireGuard interface should be handled separately
		// This just marks the device as revoked in the database
	}

	return nil
}

