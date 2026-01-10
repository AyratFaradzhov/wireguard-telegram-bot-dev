package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/joho/godotenv/autoload"

	"github.com/skoret/wireguard-bot/internal/access"
	"github.com/skoret/wireguard-bot/internal/billing"
	"github.com/skoret/wireguard-bot/internal/scheduler"
	"github.com/skoret/wireguard-bot/internal/storage"
	"github.com/skoret/wireguard-bot/internal/telegram"
)

func main() {
	// Validate required environment variables
	token := os.Getenv("TELEGRAM_APITOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_APITOKEN environment variable is required")
	}

	// Check if using SSH provisioning (Stage 2) or local provisioning (Stage 1)
	sshEnabled := os.Getenv("SSH_WG_ENABLED") == "true"
	devMode := os.Getenv("DEV_MODE") == "true"

	// For Stage 2 (SSHProvisioner), SSH variables are required
	if sshEnabled {
		sshHost := os.Getenv("SSH_WG_HOST")
		if sshHost == "" {
			log.Fatal("SSH_WG_HOST environment variable is required for Stage 2 (SSHProvisioner)")
		}

		sshUser := os.Getenv("SSH_WG_USER")
		if sshUser == "" {
			log.Fatal("SSH_WG_USER environment variable is required for Stage 2 (SSHProvisioner)")
		}

		sshKeyPath := os.Getenv("SSH_WG_KEY_PATH")
		if sshKeyPath == "" {
			log.Fatal("SSH_WG_KEY_PATH environment variable is required for Stage 2 (SSHProvisioner)")
		}
	}

	// For Stage 1 (LocalProvisioner), WIREGUARD_INTERFACE and SERVER_ENDPOINT are required
	// For Stage 2 (SSHProvisioner), these are NOT required on RU server
	if !sshEnabled && !devMode {
		wgInterface := os.Getenv("WIREGUARD_INTERFACE")
		if wgInterface == "" {
			log.Fatal("WIREGUARD_INTERFACE environment variable is required for Stage 1 (LocalProvisioner)")
		}

		serverEndpoint := os.Getenv("SERVER_ENDPOINT")
		if serverEndpoint == "" {
			log.Fatal("SERVER_ENDPOINT environment variable is required for Stage 1 (LocalProvisioner)")
		}

		dnsIPs := os.Getenv("DNS_IPS")
		if dnsIPs == "" {
			log.Fatal("DNS_IPS environment variable is required for Stage 1 (LocalProvisioner)")
		}
	}

	staticQRCode := os.Getenv("STATIC_QR_CODE")
	if staticQRCode == "" {
		log.Fatal("STATIC_QR_CODE environment variable is required")
	}

	// Initialize storage
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		dsn = "bot.db" // Default SQLite database
	}

	repo, err := storage.NewRepository(dsn)
	if err != nil {
		log.Fatalf("failed to create repository: %s", err.Error())
	}
	defer repo.Close()

	// Run migrations
	ctx := context.Background()
	if err := repo.Migrate(ctx); err != nil {
		log.Fatalf("failed to run migrations: %s", err.Error())
	}

	// Initialize billing service
	billingService := billing.NewService(repo, staticQRCode)

	// Initialize access service
	accessService := access.NewService(repo)

	// Initialize telegram bot
	tg, err := telegram.NewBot(token, repo, billingService, accessService)
	if err != nil {
		log.Fatalf("failed to create telegram bot: %s", err.Error())
	}

	// Initialize scheduler
	schedulerService := scheduler.NewService(repo, tg)

	// Start scheduler in background
	go schedulerService.Start(ctx)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := tg.Run(ctx); err != nil {
			log.Fatalf("failed to run telegram bot: %s", err.Error())
		}
	}()

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		sig := <-quit
		log.Printf("graceful shutdown with signal %v", sig)
		schedulerService.Stop()
		cancel()
		<-done
	}()
	<-done
}
