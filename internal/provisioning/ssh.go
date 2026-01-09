package provisioning

import (
	"context"
	"fmt"

	"github.com/pkg/errors"

	"github.com/skoret/wireguard-bot/internal/storage"
)

// SSHProvisioner implements Provisioner interface for remote WireGuard management via SSH
// This is a placeholder for future implementation (Stage 2)
type SSHProvisioner struct {
	repo   *storage.Repository
	config SSHConfig
}

// SSHConfig contains SSH connection configuration
type SSHConfig struct {
	Host        string
	Port        int
	User        string
	KeyPath     string
	AllowedCmds []string // Allow-listed commands for security
}

// NewSSHProvisioner creates a new SSH provisioner instance (placeholder)
// This will be implemented in Stage 2
func NewSSHProvisioner(repo *storage.Repository, config SSHConfig) (*SSHProvisioner, error) {
	return &SSHProvisioner{
		repo:   repo,
		config: config,
	}, nil
}

// Close closes the provisioner and releases resources
func (p *SSHProvisioner) Close() error {
	// TODO: Close SSH connections if any
	return nil
}

// CreateDeviceWithNewKeys creates a new device with generated keys via SSH
// This will execute allow-listed commands on remote DE server
func (p *SSHProvisioner) CreateDeviceWithNewKeys(ctx context.Context, userID, subscriptionID int64, deviceName string) (*ConfigResult, error) {
	// TODO: Stage 2 implementation
	// 1. Generate keys locally
	// 2. Reserve IP atomically in DB (RU server)
	// 3. Execute SSH command to add peer on DE server:
	//    - wg set <interface> peer <public_key> allowed-ips <ip>
	//    - wg-quick save <interface>
	// 4. Parse machine-readable output from DE server
	// 5. Generate client config locally
	return nil, errors.New("SSHProvisioner not yet implemented - will be available in Stage 2")
}

// CreateDeviceWithPublicKey creates a device with existing public key via SSH
func (p *SSHProvisioner) CreateDeviceWithPublicKey(ctx context.Context, publicKey string, userID, subscriptionID int64, deviceName string) (*ConfigResult, error) {
	// TODO: Stage 2 implementation
	return nil, errors.New("SSHProvisioner not yet implemented - will be available in Stage 2")
}

// RevokeDevice removes a device from remote WireGuard via SSH
func (p *SSHProvisioner) RevokeDevice(ctx context.Context, peerPublicKey string) error {
	// TODO: Stage 2 implementation
	// Execute SSH command:
	//   wg set <interface> peer <public_key> remove
	//   wg-quick save <interface>
	return errors.New("SSHProvisioner not yet implemented - will be available in Stage 2")
}

