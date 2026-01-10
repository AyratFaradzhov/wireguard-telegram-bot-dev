package wireguard

import (
	"context"
	"io"
	"os"
	"strconv"

	"github.com/pkg/errors"

	"github.com/skoret/wireguard-bot/internal/provisioning"
	"github.com/skoret/wireguard-bot/internal/storage"
)

// Wireguard is a wrapper around Provisioner interface
// It maintains backward compatibility while using the new provisioning abstraction
type Wireguard interface {
	io.Closer
	CreateConfigForNewKeys(ctx context.Context, userID, subscriptionID int64, deviceName string) (io.Reader, string, string, error)
	CreateConfigForPublicKey(ctx context.Context, key string, userID, subscriptionID int64, deviceName string) (io.Reader, string, error)
	// Legacy methods for backward compatibility (deprecated)
	CreateConfigForNewKeysLegacy() (io.Reader, error)
	CreateConfigForPublicKeyLegacy(key string) (io.Reader, error)
}

// wireguardWrapper wraps Provisioner to implement Wireguard interface
type wireguardWrapper struct {
	provisioner provisioning.Provisioner
}

// NewWireguard creates a new Wireguard instance using Provisioner
// Provisioner selection:
//   - DEV_MODE=true → DevProvisioner (for testing, mock implementation)
//   - otherwise → LocalProvisioner (local WireGuard via wgctrl)
func NewWireguard(repo *storage.Repository) (Wireguard, error) {
	var provisioner provisioning.Provisioner
	var err error

	// Check if using dev mode
	if devMode, _ := strconv.ParseBool(os.Getenv("DEV_MODE")); devMode {
		// Use dev provisioner (mock for testing)
		provisioner, err = NewDevProvisioner(repo)
	} else {
		// Use local provisioner (local WireGuard via wgctrl)
		provisioner, err = provisioning.NewLocalProvisioner(repo)
	}

	if err != nil {
		return nil, errors.Wrap(err, "failed to create provisioner")
	}

	return NewWireguardFromProvisioner(provisioner), nil
}

// NewWireguardFromProvisioner creates a Wireguard instance from a Provisioner
func NewWireguardFromProvisioner(provisioner provisioning.Provisioner) Wireguard {
	return &wireguardWrapper{
		provisioner: provisioner,
	}
}

// Close closes the wireguard instance
func (w *wireguardWrapper) Close() error {
	if w.provisioner != nil {
		return w.provisioner.Close()
	}
	return nil
}

// CreateConfigForNewKeys creates a config for new keys
func (w *wireguardWrapper) CreateConfigForNewKeys(ctx context.Context, userID, subscriptionID int64, deviceName string) (io.Reader, string, string, error) {
	result, err := w.provisioner.CreateDeviceWithNewKeys(ctx, userID, subscriptionID, deviceName)
	if err != nil {
		return nil, "", "", err
	}
	return result.ConfigReader, result.PublicKey, result.AssignedIP, nil
}

// CreateConfigForPublicKey creates a config for existing public key
func (w *wireguardWrapper) CreateConfigForPublicKey(ctx context.Context, key string, userID, subscriptionID int64, deviceName string) (io.Reader, string, error) {
	result, err := w.provisioner.CreateDeviceWithPublicKey(ctx, key, userID, subscriptionID, deviceName)
	if err != nil {
		return nil, "", err
	}
	return result.ConfigReader, result.AssignedIP, nil
}

// Legacy methods

func (w *wireguardWrapper) CreateConfigForNewKeysLegacy() (io.Reader, error) {
	ctx := context.Background()
	reader, _, _, err := w.CreateConfigForNewKeys(ctx, 0, 0, "legacy")
	return reader, err
}

func (w *wireguardWrapper) CreateConfigForPublicKeyLegacy(key string) (io.Reader, error) {
	ctx := context.Background()
	reader, _, err := w.CreateConfigForPublicKey(ctx, key, 0, 0, "legacy")
	return reader, err
}
