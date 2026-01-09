package provisioning

import (
	"context"
	"io"
)

// DeviceConfig represents a device configuration that needs to be provisioned
type DeviceConfig struct {
	UserID        int64
	SubscriptionID int64
	DeviceName    string
	PeerPublicKey string
	AssignedIP    string
}

// ConfigResult represents the result of config creation
type ConfigResult struct {
	ConfigReader io.Reader
	PublicKey    string // For new keys generation
	AssignedIP   string
}

// Provisioner is an interface for provisioning WireGuard devices
// It abstracts the implementation details (local vs SSH-based)
type Provisioner interface {
	// CreateDeviceWithNewKeys creates a new device with generated keys
	// Returns the client config, public key, and assigned IP
	CreateDeviceWithNewKeys(ctx context.Context, userID, subscriptionID int64, deviceName string) (*ConfigResult, error)

	// CreateDeviceWithPublicKey creates a device with existing public key
	// Returns the client config and assigned IP
	CreateDeviceWithPublicKey(ctx context.Context, publicKey string, userID, subscriptionID int64, deviceName string) (*ConfigResult, error)

	// RevokeDevice removes a device from WireGuard
	RevokeDevice(ctx context.Context, peerPublicKey string) error

	// Close closes the provisioner and releases resources
	Close() error
}

