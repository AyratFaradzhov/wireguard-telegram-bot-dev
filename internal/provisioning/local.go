package provisioning

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"

	_ "github.com/joho/godotenv/autoload"
	"github.com/pkg/errors"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/skoret/wireguard-bot/internal/storage"
	cfgs "github.com/skoret/wireguard-bot/internal/wireguard/configs"
)

// LocalProvisioner implements Provisioner interface for local WireGuard management
type LocalProvisioner struct {
	device string
	dns    []string
	client *wgctrl.Client
	repo   *storage.Repository
}

// NewLocalProvisioner creates a new local provisioner instance
func NewLocalProvisioner(repo *storage.Repository) (*LocalProvisioner, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create wgctrl client")
	}

	devs, err := client.Devices()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list devices")
	}

	log.Printf("--- known devices: ---")
	for i, d := range devs {
		log.Printf("#%d device: %+v", i, d)
	}
	log.Printf("----------------------")

	// Get and validate WIREGUARD_INTERFACE
	wgInterface := os.Getenv("WIREGUARD_INTERFACE")
	if wgInterface == "" {
		return nil, errors.New("WIREGUARD_INTERFACE environment variable is required")
	}

	// Verify that the interface exists
	found := false
	for _, d := range devs {
		if d.Name == wgInterface {
			found = true
			break
		}
	}
	if !found {
		return nil, errors.Errorf("WireGuard interface '%s' not found. Available interfaces: %v", wgInterface, getDeviceNames(devs))
	}

	log.Printf("Using WireGuard interface: %s", wgInterface)

	// Get and validate DNS_IPS
	dns := os.Getenv("DNS_IPS")
	if dns == "" {
		return nil, errors.New("DNS_IPS environment variable is required")
	}
	var dnsList []string
	dnsSplit := strings.Split(dns, ",")
	for _, d := range dnsSplit {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		// Basic validation: check if it's a valid IP
		if net.ParseIP(d) == nil {
			return nil, errors.Errorf("invalid DNS IP address: %s", d)
		}
		dnsList = append(dnsList, d)
	}
	if len(dnsList) == 0 {
		return nil, errors.New("at least one valid DNS_IPS is required")
	}

	return &LocalProvisioner{
		device: wgInterface,
		dns:    dnsList,
		client: client,
		repo:   repo,
	}, nil
}

// getDeviceNames returns list of device names
func getDeviceNames(devs []*wgctrl.Device) []string {
	names := make([]string, len(devs))
	for i, d := range devs {
		names[i] = d.Name
	}
	return names
}

// Close closes the provisioner and releases resources
func (p *LocalProvisioner) Close() error {
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// CreateDeviceWithNewKeys creates a new device with generated keys
func (p *LocalProvisioner) CreateDeviceWithNewKeys(ctx context.Context, userID, subscriptionID int64, deviceName string) (*ConfigResult, error) {
	pri, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate private key")
	}
	pub := pri.PublicKey()

	// Atomically reserve IP through DB transaction
	tx, err := p.repo.BeginTx(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	ipNet, err := p.getNextIPNetAtomic(ctx, tx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get next IP")
	}

	// Check if peer already exists
	existing, err := p.repo.GetDeviceByPeerPublicKey(ctx, pub.String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to check existing device")
	}
	if existing != nil {
		return nil, errors.New("device with this public key already exists")
	}

	// Create device record in DB
	device := &storage.Device{
		UserID:         userID,
		SubscriptionID: subscriptionID,
		DeviceName:     deviceName,
		PeerPublicKey:  pub.String(),
		AssignedIP:     ipNet.IP.String(),
	}

	// Insert device
	_, err = tx.ExecContext(ctx,
		`INSERT INTO devices (user_id, subscription_id, device_name, peer_public_key, assigned_ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		device.UserID, device.SubscriptionID, device.DeviceName, device.PeerPublicKey,
		device.AssignedIP, storage.GetTime(),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to insert device")
	}

	// Commit transaction before updating WireGuard interface
	if err := tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "failed to commit transaction")
	}

	// Create client config
	cfgFile, err := p.createConfig(pri.String(), ipNet)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create config")
	}

	// Update WireGuard device configuration
	if err := p.updateDevice(pub, ipNet); err != nil {
		// Log error but don't fail - device is already in DB
		log.Printf("Warning: failed to update WireGuard device after DB commit: %v", err)
	}

	// Get device ID from DB
	device, err = p.repo.GetDeviceByPeerPublicKey(ctx, pub.String())
	if err != nil || device == nil {
		return nil, errors.New("failed to retrieve created device")
	}

	return &ConfigResult{
		ConfigReader: cfgFile,
		PublicKey:    pub.String(),
		AssignedIP:   ipNet.IP.String(),
	}, nil
}

// CreateDeviceWithPublicKey creates a device with existing public key
func (p *LocalProvisioner) CreateDeviceWithPublicKey(ctx context.Context, publicKey string, userID, subscriptionID int64, deviceName string) (*ConfigResult, error) {
	pub, err := wgtypes.ParseKey(publicKey)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse public key")
	}

	// Atomically reserve IP through DB transaction
	tx, err := p.repo.BeginTx(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	ipNet, err := p.getNextIPNetAtomic(ctx, tx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get next IP")
	}

	// Check if peer already exists
	existing, err := p.repo.GetDeviceByPeerPublicKey(ctx, pub.String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to check existing device")
	}
	if existing != nil {
		return nil, errors.New("device with this public key already exists")
	}

	// Create device record in DB
	device := &storage.Device{
		UserID:         userID,
		SubscriptionID: subscriptionID,
		DeviceName:     deviceName,
		PeerPublicKey:  pub.String(),
		AssignedIP:     ipNet.IP.String(),
	}

	// Insert device
	_, err = tx.ExecContext(ctx,
		`INSERT INTO devices (user_id, subscription_id, device_name, peer_public_key, assigned_ip, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		device.UserID, device.SubscriptionID, device.DeviceName, device.PeerPublicKey,
		device.AssignedIP, storage.GetTime(),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to insert device")
	}

	// Commit transaction before updating WireGuard interface
	if err := tx.Commit(); err != nil {
		return nil, errors.Wrap(err, "failed to commit transaction")
	}

	// Create client config (without private key)
	cfgFile, err := p.createConfig("", ipNet)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create config")
	}

	// Update WireGuard device configuration
	if err := p.updateDevice(pub, ipNet); err != nil {
		log.Printf("Warning: failed to update WireGuard device after DB commit: %v", err)
	}

	return &ConfigResult{
		ConfigReader: cfgFile,
		AssignedIP:   ipNet.IP.String(),
	}, nil
}

// RevokeDevice removes a device from WireGuard
func (p *LocalProvisioner) RevokeDevice(ctx context.Context, peerPublicKey string) error {
	// Parse public key
	pub, err := wgtypes.ParseKey(peerPublicKey)
	if err != nil {
		return errors.Wrap(err, "failed to parse public key")
	}

	// Remove peer from WireGuard interface
	cfg := wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey: pub,
				Remove:    true,
			},
		},
	}

	if err := p.client.ConfigureDevice(p.device, cfg); err != nil {
		return errors.Wrap(err, "failed to remove peer from WireGuard")
	}

	// Save configuration
	cmd := exec.Command("wg-quick", "save", p.device)
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "failed to save WireGuard config")
	}

	return nil
}

// getNextIPNetAtomic gets next IP atomically within a transaction
func (p *LocalProvisioner) getNextIPNetAtomic(ctx context.Context, tx *sql.Tx) (*net.IPNet, error) {
	// Get latest assigned IP from DB (atomic within transaction)
	var latestIPStr sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT assigned_ip FROM devices WHERE revoked_at IS NULL ORDER BY assigned_ip DESC LIMIT 1`,
	).Scan(&latestIPStr)

	var lastIP net.IP
	if err != nil && err != sql.ErrNoRows {
		// Fallback to getting from WireGuard interface
		lastIP, err = p.getLatestUsedIP()
		if err != nil {
			return nil, err
		}
	} else if latestIPStr.Valid {
		// Parse IP from DB
		lastIP = net.ParseIP(latestIPStr.String)
		if lastIP == nil {
			// Invalid IP in DB, fallback to WireGuard interface
			lastIP, err = p.getLatestUsedIP()
			if err != nil {
				return nil, err
			}
		}
	} else {
		// No IPs in DB, get base IP from WireGuard interface
		lastIP, err = p.getDeviceAddress()
		if err != nil {
			return nil, err
		}
	}

	nextIPAddr := p.nextIP(lastIP, 1)

	return &net.IPNet{
		IP:   nextIPAddr,
		Mask: net.IPv4Mask(255, 255, 255, 255),
	}, nil
}

// createConfig creates a client configuration file
func (p *LocalProvisioner) createConfig(pri string, ipNet *net.IPNet) (io.Reader, error) {
	device, err := p.client.Device(p.device)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get device "+p.device)
	}

	clientConfig := cfgs.ClientConfig{
		Address:    ipNet.String(),
		PrivateKey: pri,
		DNS:        p.dns,
		PublicKey:  device.PublicKey.String(),
		AllowedIPs: []string{"0.0.0.0/0"},
		Endpoint:   os.Getenv("SERVER_ENDPOINT"),
	}

	cfgFile, err := cfgs.ProcessClientConfig(clientConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to process client config")
	}

	return cfgFile, nil
}

// updateDevice updates WireGuard device configuration
func (p *LocalProvisioner) updateDevice(pub wgtypes.Key, ipNet *net.IPNet) error {
	cfg := wgtypes.Config{
		Peers: []wgtypes.PeerConfig{
			{
				PublicKey:  pub,
				AllowedIPs: []net.IPNet{*ipNet},
			},
		},
	}

	if err := p.client.ConfigureDevice(p.device, cfg); err != nil {
		return errors.Wrap(err, "failed to update server configuration")
	}

	cmd := exec.Command("wg-quick", "save", p.device)
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "failed to dump server config to conf file")
	}

	return nil
}

// getLatestUsedIP gets the latest used IP from WireGuard interface
func (p *LocalProvisioner) getLatestUsedIP() (net.IP, error) {
	device, err := p.client.Device(p.device)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get device "+p.device)
	}

	lastIP, err := p.getDeviceAddress()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get address for device "+p.device)
	}

	for _, peer := range device.Peers {
		for _, ipNet := range peer.AllowedIPs {
			if bytes.Compare(ipNet.IP, lastIP) >= 0 {
				lastIP = ipNet.IP
			}
		}
	}

	if lastIP == nil {
		return nil, errors.New("failed to get latest used ip for device " + p.device)
	}

	return lastIP, nil
}

// getDeviceAddress gets the base IP address of the WireGuard interface
func (p *LocalProvisioner) getDeviceAddress() (net.IP, error) {
	ife, err := net.InterfaceByName(p.device)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get interface "+p.device)
	}

	addrs, err := ife.Addrs()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get address for interface "+p.device)
	}

	for _, addr := range addrs {
		if ipv4Addr := addr.(*net.IPNet).IP.To4(); ipv4Addr != nil {
			return ipv4Addr, nil
		}
	}

	return nil, errors.New("failed to get address for interface " + p.device)
}

// nextIP increments an IP address
// Thanks to https://gist.github.com/udhos/b468fbfd376aa0b655b6b0c539a88c03
func (p *LocalProvisioner) nextIP(ip net.IP, inc uint) net.IP {
	i := ip.To4()
	v := uint(i[0])<<24 + uint(i[1])<<16 + uint(i[2])<<8 + uint(i[3])
	v += inc
	v3 := byte(v & 0xFF)
	v2 := byte((v >> 8) & 0xFF)
	v1 := byte((v >> 16) & 0xFF)
	v0 := byte((v >> 24) & 0xFF)
	return net.IPv4(v0, v1, v2, v3)
}

