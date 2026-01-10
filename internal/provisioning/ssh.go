package provisioning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	_ "github.com/joho/godotenv/autoload"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/skoret/wireguard-bot/internal/storage"
	cfgs "github.com/skoret/wireguard-bot/internal/wireguard/configs"
)

// SSHProvisioner implements Provisioner interface for remote WireGuard management via SSH
// Stage 2: RU server manages DE server WireGuard via SSH
type SSHProvisioner struct {
	repo      *storage.Repository
	config    SSHConfig
	sshClient *ssh.Client
}

// SSHConfig contains SSH connection configuration
type SSHConfig struct {
	Host    string
	Port    int
	User    string
	KeyPath string
}

// createResponse represents the JSON response from wg-provision create command
// DE server is the single source of truth for assigned_ip
// DE server returns only data, NOT client_config (RU generates it)
type createResponse struct {
	AssignedIP      string `json:"assigned_ip"`
	ServerPublicKey string `json:"server_public_key"`
	Endpoint        string `json:"endpoint"`
	DNS             string `json:"dns"`
}

// revokeResponse represents the JSON response from wg-provision revoke command
type revokeResponse struct {
	OK bool `json:"ok"`
}

// NewSSHProvisioner creates a new SSH provisioner instance
// Reads configuration from environment variables:
// - SSH_WG_HOST: DE server hostname or IP
// - SSH_WG_PORT: SSH port (default: 22)
// - SSH_WG_USER: SSH username
// - SSH_WG_KEY_PATH: Path to SSH private key file
func NewSSHProvisioner(repo *storage.Repository) (*SSHProvisioner, error) {
	host := os.Getenv("SSH_WG_HOST")
	if host == "" {
		return nil, errors.New("SSH_WG_HOST environment variable is required")
	}

	port := 22
	if portStr := os.Getenv("SSH_WG_PORT"); portStr != "" {
		var err error
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return nil, errors.Wrap(err, "invalid SSH_WG_PORT")
		}
	}

	user := os.Getenv("SSH_WG_USER")
	if user == "" {
		return nil, errors.New("SSH_WG_USER environment variable is required")
	}

	keyPath := os.Getenv("SSH_WG_KEY_PATH")
	if keyPath == "" {
		return nil, errors.New("SSH_WG_KEY_PATH environment variable is required")
	}

	config := SSHConfig{
		Host:    host,
		Port:    port,
		User:    user,
		KeyPath: keyPath,
	}

	return &SSHProvisioner{
		repo:   repo,
		config: config,
	}, nil
}

// getSSHClient creates or returns existing SSH client connection
func (p *SSHProvisioner) getSSHClient(ctx context.Context) (*ssh.Client, error) {
	if p.sshClient != nil {
		// Check if connection is still alive
		_, _, err := p.sshClient.SendRequest("keepalive", false, nil)
		if err == nil {
			return p.sshClient, nil
		}
		// Connection is dead, close it
		p.sshClient.Close()
		p.sshClient = nil
	}

	// Read private key
	key, err := os.ReadFile(p.config.KeyPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read SSH key from %s", p.config.KeyPath)
	}

	// Parse private key
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse SSH private key")
	}

	// Create SSH config
	sshConfig := &ssh.ClientConfig{
		User:            p.config.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // In production, use ssh.FixedHostKey
		Timeout:         0,
	}

	// Connect to SSH server
	addr := fmt.Sprintf("%s:%d", p.config.Host, p.config.Port)
	client, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to connect to SSH server %s", addr)
	}

	p.sshClient = client
	return client, nil
}

// executeSSHCommand executes an allow-listed command via SSH
// Only /usr/local/bin/wg-provision is allowed for security
// Uses direct command execution without shell to prevent injection
func (p *SSHProvisioner) executeSSHCommand(ctx context.Context, args []string) ([]byte, error) {
	client, err := p.getSSHClient(ctx)
	if err != nil {
		return nil, err
	}

	// Sanitize arguments - no shell injection
	for _, arg := range args {
		if strings.Contains(arg, "\n") || strings.Contains(arg, "\r") || strings.Contains(arg, "\x00") {
			return nil, errors.New("invalid argument contains control characters")
		}
	}

	// Build command: only allow /usr/local/bin/wg-provision
	// Use strconv.Quote for each argument to prevent shell injection
	cmd := "/usr/local/bin/wg-provision"
	for _, arg := range args {
		cmd += " " + strconv.Quote(arg)
	}

	// Create session
	session, err := client.NewSession()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create SSH session")
	}
	defer session.Close()

	// Execute command and capture output
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Execute command (quoted arguments prevent shell injection)
	err = session.Run(cmd)
	if err != nil {
		stderrStr := stderr.String()
		if stderrStr != "" {
			return nil, errors.Wrapf(err, "SSH command failed: %s", stderrStr)
		}
		return nil, errors.Wrap(err, "SSH command failed")
	}

	return stdout.Bytes(), nil
}

// Close closes the provisioner and releases resources
func (p *SSHProvisioner) Close() error {
	if p.sshClient != nil {
		return p.sshClient.Close()
	}
	return nil
}

// parseAssignedIP parses assigned_ip from DE server response
// Removes /32 suffix if present, returns clean IP address
func parseAssignedIP(assignedIP string) (string, error) {
	if assignedIP == "" {
		return "", errors.New("assigned_ip is empty")
	}
	// Remove /32 suffix if present
	ip := strings.TrimSuffix(assignedIP, "/32")
	// Basic validation - should not contain slash after removal
	if strings.Contains(ip, "/") {
		return "", errors.Errorf("invalid assigned_ip format: %s (should be IP or IP/32)", assignedIP)
	}
	if net.ParseIP(ip) == nil {
		return "", errors.Errorf("invalid IP address: %s", ip)
	}
	return ip, nil
}

// createClientConfig creates client WireGuard config using templates
// RU server generates config from DE server response data
func (p *SSHProvisioner) createClientConfig(privateKey, assignedIP, serverPublicKey, endpoint, dns string) (io.Reader, error) {
	// Parse DNS (can be comma-separated)
	dnsList := []string{dns}
	if strings.Contains(dns, ",") {
		dnsList = strings.Split(dns, ",")
		for i := range dnsList {
			dnsList[i] = strings.TrimSpace(dnsList[i])
		}
	}

	// Ensure assignedIP has /32 suffix for client config
	clientIP := assignedIP
	if !strings.Contains(clientIP, "/") {
		clientIP = assignedIP + "/32"
	}

	// Create client config using existing templates
	clientConfig := cfgs.ClientConfig{
		Address:    clientIP,
		PrivateKey: privateKey,
		DNS:        dnsList,
		PublicKey:  serverPublicKey,
		AllowedIPs: []string{"0.0.0.0/0"},
		Endpoint:   endpoint,
	}

	cfgReader, err := cfgs.ProcessClientConfig(clientConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate client config from template")
	}

	return cfgReader, nil
}

// CreateDeviceWithNewKeys creates a new device with generated keys via SSH
// DE server is the single source of truth for assigned_ip
// 1. Generate keys locally on RU server
// 2. Check if peer already exists in DB
// 3. Execute SSH command to create peer on DE server
// 4. Parse JSON response: get assigned_ip, server_public_key, endpoint, dns from DE server
// 5. Save device to DB with assigned_ip from DE server (single source of truth)
// 6. Generate client_config on RU server using templates
func (p *SSHProvisioner) CreateDeviceWithNewKeys(ctx context.Context, userID, subscriptionID int64, deviceName string) (*ConfigResult, error) {
	// Generate keys locally on RU server
	pri, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate private key")
	}
	pub := pri.PublicKey()

	// Check if peer already exists (before calling DE server)
	existing, err := p.repo.GetDeviceByPeerPublicKey(ctx, pub.String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to check existing device")
	}
	if existing != nil {
		return nil, errors.New("device with this public key already exists")
	}

	// Execute SSH command on DE server: wg-provision create
	// DE server is the single source of truth for assigned_ip
	args := []string{
		"create",
		"--user-id", strconv.FormatInt(userID, 10),
		"--subscription-id", strconv.FormatInt(subscriptionID, 10),
		"--device-name", deviceName,
		"--public-key", pub.String(),
	}

	output, err := p.executeSSHCommand(ctx, args)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create peer on DE server")
	}

	// Parse JSON response from DE server
	var resp createResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, errors.Wrapf(err, "failed to parse JSON response from DE server: %s", string(output))
	}

	// Validate response
	if resp.AssignedIP == "" || resp.ServerPublicKey == "" || resp.Endpoint == "" || resp.DNS == "" {
		return nil, errors.Errorf("invalid response from DE server: missing required fields (assigned_ip, server_public_key, endpoint, dns)")
	}

	// Parse assigned_ip from DE server (single source of truth)
	assignedIP, err := parseAssignedIP(resp.AssignedIP)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse assigned_ip from DE server")
	}

	// Save device to DB with assigned_ip from DE server (single source of truth)
	device := &storage.Device{
		UserID:         userID,
		SubscriptionID: subscriptionID,
		DeviceName:     deviceName,
		PeerPublicKey:  pub.String(),
		AssignedIP:     assignedIP, // From DE server - single source of truth
	}

	// Insert device to DB using repository method
	err = p.repo.CreateDevice(ctx, device)
	if err != nil {
		return nil, errors.Wrap(err, "failed to insert device to DB")
	}

	// Generate client_config on RU server using templates
	configReader, err := p.createClientConfig(pri.String(), assignedIP, resp.ServerPublicKey, resp.Endpoint, resp.DNS)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate client config")
	}

	return &ConfigResult{
		ConfigReader: configReader,
		PublicKey:    pub.String(),
		AssignedIP:   assignedIP,
	}, nil
}

// CreateDeviceWithPublicKey creates a device with existing public key via SSH
// DE server is the single source of truth for assigned_ip
// Similar to CreateDeviceWithNewKeys but uses provided public key (no private key generation)
func (p *SSHProvisioner) CreateDeviceWithPublicKey(ctx context.Context, publicKey string, userID, subscriptionID int64, deviceName string) (*ConfigResult, error) {
	pub, err := wgtypes.ParseKey(publicKey)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse public key")
	}

	// Check if peer already exists (before calling DE server)
	existing, err := p.repo.GetDeviceByPeerPublicKey(ctx, pub.String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to check existing device")
	}
	if existing != nil {
		return nil, errors.New("device with this public key already exists")
	}

	// Execute SSH command on DE server: wg-provision create
	// DE server is the single source of truth for assigned_ip
	args := []string{
		"create",
		"--user-id", strconv.FormatInt(userID, 10),
		"--subscription-id", strconv.FormatInt(subscriptionID, 10),
		"--device-name", deviceName,
		"--public-key", pub.String(),
	}

	output, err := p.executeSSHCommand(ctx, args)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create peer on DE server")
	}

	// Parse JSON response from DE server
	var resp createResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, errors.Wrapf(err, "failed to parse JSON response from DE server: %s", string(output))
	}

	// Validate response
	if resp.AssignedIP == "" || resp.ServerPublicKey == "" || resp.Endpoint == "" || resp.DNS == "" {
		return nil, errors.Errorf("invalid response from DE server: missing required fields (assigned_ip, server_public_key, endpoint, dns)")
	}

	// Parse assigned_ip from DE server (single source of truth)
	assignedIP, err := parseAssignedIP(resp.AssignedIP)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse assigned_ip from DE server")
	}

	// Save device to DB with assigned_ip from DE server (single source of truth)
	device := &storage.Device{
		UserID:         userID,
		SubscriptionID: subscriptionID,
		DeviceName:     deviceName,
		PeerPublicKey:  pub.String(),
		AssignedIP:     assignedIP, // From DE server - single source of truth
	}

	// Insert device to DB using repository method
	err = p.repo.CreateDevice(ctx, device)
	if err != nil {
		return nil, errors.Wrap(err, "failed to insert device to DB")
	}

	// Generate client_config on RU server using templates
	// Note: private key is not available in this case, will be placeholder in config
	configReader, err := p.createClientConfig("", assignedIP, resp.ServerPublicKey, resp.Endpoint, resp.DNS)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate client config")
	}

	return &ConfigResult{
		ConfigReader: configReader,
		AssignedIP:   assignedIP,
	}, nil
}

// RevokeDevice removes a device from remote WireGuard via SSH
// 1. Get assigned_ip from DB by peerPublicKey
// 2. Execute SSH command: wg-provision revoke --assigned-ip <ip>
// 3. Verify response {ok: true}
func (p *SSHProvisioner) RevokeDevice(ctx context.Context, peerPublicKey string) error {
	// Get device from DB to find assigned_ip
	device, err := p.repo.GetDeviceByPeerPublicKey(ctx, peerPublicKey)
	if err != nil {
		return errors.Wrap(err, "failed to get device from DB")
	}
	if device == nil {
		return errors.New("device not found in DB")
	}

	if device.AssignedIP == "" {
		return errors.New("device has no assigned IP")
	}

	// Execute SSH command on DE server: wg-provision revoke
	args := []string{
		"revoke",
		"--assigned-ip", device.AssignedIP,
	}

	output, err := p.executeSSHCommand(ctx, args)
	if err != nil {
		return errors.Wrap(err, "failed to revoke peer on DE server")
	}

	// Parse JSON response
	var resp revokeResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return errors.Wrapf(err, "failed to parse JSON response from DE server: %s", string(output))
	}

	// Verify response
	if !resp.OK {
		return errors.New("DE server returned ok=false")
	}

	return nil
}


