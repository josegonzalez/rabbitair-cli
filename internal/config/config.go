// Package config stores saved devices in the XDG config home and their
// tokens in the OS keychain, falling back to the config file when no
// keychain is usable.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/zalando/go-keyring"
)

// KeyringService is the service name tokens are stored under.
const KeyringService = "rabbitair-cli"

// Device is a saved device.
type Device struct {
	Host     string `toml:"host"`
	MAC      string `toml:"mac,omitempty"`
	Location string `toml:"location,omitempty"`
	// Token is set only when the token is stored in the config file.
	Token string `toml:"token,omitempty"`
}

// Config is the contents of the config file.
type Config struct {
	Default string             `toml:"default,omitempty"`
	Devices map[string]*Device `toml:"devices,omitempty"`

	path string
}

// Path returns the config file path: $XDG_CONFIG_HOME/rabbitair-cli/config.toml,
// or ~/.config/rabbitair-cli/config.toml when XDG_CONFIG_HOME is unset.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "rabbitair-cli", "config.toml"), nil
}

// Load reads the config file. A missing file yields an empty config.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	c := &Config{path: path, Devices: map[string]*Device{}}

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "warning: %s is accessible by other users; run: chmod 600 %s\n", path, path)
	}
	if _, err := toml.DecodeFile(path, c); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if c.Devices == nil {
		c.Devices = map[string]*Device{}
	}
	return c, nil
}

// File returns the path the config was loaded from.
func (c *Config) File() string { return c.path }

// Save writes the config file with 0600 permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

// Names returns the saved device names, sorted.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Devices))
	for n := range c.Devices {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Lookup finds a device by name, then by host, MAC or location.
func (c *Config) Lookup(key string) (string, *Device, bool) {
	if d, ok := c.Devices[key]; ok {
		return key, d, true
	}
	for _, n := range c.Names() {
		d := c.Devices[n]
		if strings.EqualFold(d.Host, key) ||
			(d.MAC != "" && strings.EqualFold(d.MAC, NormalizeMAC(key))) ||
			(d.Location != "" && strings.EqualFold(d.Location, key)) {
			return n, d, true
		}
	}
	return "", nil, false
}

// NormalizeMAC uppercases a MAC address and strips separators.
func NormalizeMAC(mac string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
}

func account(name string, d *Device) string {
	if d.MAC != "" {
		return d.MAC
	}
	return "name:" + name
}

// Storage describes where a token is stored.
type Storage string

// Token storage locations.
const (
	StorageKeychain Storage = "keychain"
	StorageFile     Storage = "config file"
)

// SetToken stores the token in the keychain, or in the config file when
// the keychain is unusable or insecure is true. The config must be saved
// afterwards.
func (c *Config) SetToken(name string, d *Device, token string, insecure bool) (Storage, error) {
	if !insecure {
		err := keyring.Set(KeyringService, account(name, d), token)
		if err == nil {
			d.Token = ""
			return StorageKeychain, nil
		}
		fmt.Fprintf(os.Stderr, "warning: keychain unavailable (%v); storing token in %s\n", err, c.path)
	}
	d.Token = token
	return StorageFile, nil
}

// Token returns the token for a device and where it was found.
func (c *Config) Token(name string, d *Device) (string, Storage, error) {
	if d.Token != "" {
		return d.Token, StorageFile, nil
	}
	token, err := keyring.Get(KeyringService, account(name, d))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", "", fmt.Errorf("no token saved for %q; run: rabbitair-cli config add %s --token <token>", name, name)
		}
		return "", "", fmt.Errorf("read token for %q from keychain: %w", name, err)
	}
	return token, StorageKeychain, nil
}

// Remove deletes a device and its keychain entry.
func (c *Config) Remove(name string) error {
	d, ok := c.Devices[name]
	if !ok {
		return fmt.Errorf("no device named %q", name)
	}
	if d.Token == "" {
		if err := keyring.Delete(KeyringService, account(name, d)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "warning: could not remove keychain entry: %v\n", err)
		}
	}
	delete(c.Devices, name)
	if c.Default == name {
		c.Default = ""
	}
	return nil
}
