package wireguard

import (
	"context"
	"io"
	"log"

	"github.com/skoret/wireguard-bot/internal/provisioning"
	cfgs "github.com/skoret/wireguard-bot/internal/wireguard/configs"
	"github.com/skoret/wireguard-bot/internal/storage"
)

// DevProvisioner is a mock provisioner for development/testing
type DevProvisioner struct{}

// NewDevProvisioner creates a new dev provisioner
func NewDevProvisioner(repo *storage.Repository) (provisioning.Provisioner, error) {
	log.Println("--- create dummy dev provisioner ---")
	return &DevProvisioner{}, nil
}

func (d *DevProvisioner) Close() error {
	log.Println("dev provisioner closed")
	return nil
}

func (d *DevProvisioner) CreateDeviceWithNewKeys(ctx context.Context, userID, subscriptionID int64, deviceName string) (*provisioning.ConfigResult, error) {
	log.Printf("dev provisioner creates dummy config for user %d, subscription %d, device %s", userID, subscriptionID, deviceName)
	cfg := cfgs.ClientConfig{
		Address:    "10.0.0.1/32",
		PrivateKey: "dummy_private_key",
		DNS:        []string{"8.8.8.8"},
		PublicKey:  "dummy_public_key",
		AllowedIPs: []string{"0.0.0.0/0"},
		Endpoint:   "127.0.0.1:51820",
	}
	reader, err := cfgs.ProcessClientConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &provisioning.ConfigResult{
		ConfigReader: reader,
		PublicKey:    "dummy_public_key",
		AssignedIP:   "10.0.0.1",
	}, nil
}

func (d *DevProvisioner) CreateDeviceWithPublicKey(ctx context.Context, key string, userID, subscriptionID int64, deviceName string) (*provisioning.ConfigResult, error) {
	log.Printf("dev provisioner creates dummy config for public key %s, user %d, subscription %d, device %s", key, userID, subscriptionID, deviceName)
	cfg := cfgs.ClientConfig{
		Address:    "10.0.0.1/32",
		PrivateKey: "",
		DNS:        []string{"8.8.8.8"},
		PublicKey:  "dummy_server_public_key",
		AllowedIPs: []string{"0.0.0.0/0"},
		Endpoint:   "127.0.0.1:51820",
	}
	reader, err := cfgs.ProcessClientConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &provisioning.ConfigResult{
		ConfigReader: reader,
		AssignedIP:   "10.0.0.1",
	}, nil
}

func (d *DevProvisioner) RevokeDevice(ctx context.Context, peerPublicKey string) error {
	log.Printf("dev provisioner revokes device with key %s", peerPublicKey)
	return nil
}
