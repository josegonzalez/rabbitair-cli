package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/josegonzalez/rabbitair-cli/internal/config"
	"github.com/josegonzalez/rabbitair-cli/rabbitair"
)

const (
	oldKey = "00000000000000000000000000000001"
	newKey = "00000000000000000000000000000002"
)

// fakeNetwork emulates devices whose accepted keys can change.
type fakeNetwork struct {
	devices  []rabbitair.Device
	keys     map[string]string // address -> accepted key
	macs     map[string]string // address -> MAC reported by info
	discover int
	onRepair func(n *fakeNetwork)
}

func (n *fakeNetwork) setupper(t *testing.T, input string) (*setupper, *bytes.Buffer) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	s := &setupper{
		in:  bufio.NewReader(strings.NewReader(input)),
		out: out,
		discover: func(context.Context) ([]rabbitair.Device, error) {
			n.discover++
			if n.discover > 1 && n.onRepair != nil {
				n.onRepair(n)
				n.onRepair = nil
			}
			return n.devices, nil
		},
		verify: func(_ context.Context, host, token string) (*rabbitair.Info, error) {
			if n.keys[host] != token {
				return nil, errors.New("timeout")
			}
			return &rabbitair.Info{MAC: n.macs[host]}, nil
		},
		cfg:        cfg,
		checkSaved: true,
	}
	s.readSecret = func(ctx context.Context, prompt string) (string, error) { return s.ask(ctx, prompt) }
	return s, out
}

func setupEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keyring.MockInit()
}

func newNetwork() *fakeNetwork {
	return &fakeNetwork{
		devices: []rabbitair.Device{{Name: "thing188", Address: "192.168.1.188", Port: 9009, ID: "8CAAB5AAE3A4"}},
		keys:    map[string]string{"192.168.1.188": newKey},
		macs:    map[string]string{"192.168.1.188": "8C:AA:B5:AA:E3:A4"},
	}
}

func TestSetupNewDevice(t *testing.T) {
	setupEnv(t)
	n := newNetwork()
	s, out := n.setupper(t, "bedroom\nLiving Room\n"+newKey+"\n")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err, out)
	}

	cfg, _ := config.Load()
	d, ok := cfg.Devices["bedroom"]
	if !ok || d.MAC != "8CAAB5AAE3A4" || d.Host != "192.168.1.188" || d.Location != "Living Room" || cfg.Default != "bedroom" {
		t.Fatalf("not saved correctly: %+v\n%s", cfg, out)
	}
	if tok, storage, _ := cfg.Token("bedroom", d); tok != newKey || storage != config.StorageKeychain {
		t.Fatalf("token = %q in %s", tok, storage)
	}
	for _, want := range []string{"Rabbit Air 2 app", "Older Rabbit Air app", "Thing ID thing188"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSetupDefaultName(t *testing.T) {
	setupEnv(t)
	n := newNetwork()
	s, out := n.setupper(t, "\n\n"+newKey+"\n")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if _, ok := cfg.Devices["purifier-aae3a4"]; !ok {
		t.Fatalf("default name not used:\n%s", out)
	}
}

func TestSetupRepair(t *testing.T) {
	setupEnv(t)
	n := newNetwork()
	n.keys["192.168.1.188"] = "unusable"
	n.onRepair = func(n *fakeNetwork) {
		n.devices[0].Name = "thing188-new"
		n.keys["192.168.1.188"] = newKey
	}

	s, out := n.setupper(t, "bedroom\n\n"+oldKey+"\nr\n"+newKey+"\n")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err, out)
	}
	for _, want := range []string{"did not accept that key", "Re-pair it in the app", "Thing ID changed to thing188-new", "Saved \"bedroom\""} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSetupStaleSavedKey(t *testing.T) {
	setupEnv(t)
	cfg, _ := config.Load()
	dev := &config.Device{Host: "192.168.1.188", MAC: "8CAAB5AAE3A4", Location: "Living Room"}
	cfg.SetToken("bedroom", dev, oldKey, false)
	cfg.Devices["bedroom"] = dev
	cfg.Default = "bedroom"
	cfg.Save()

	n := newNetwork()
	s, out := n.setupper(t, newKey+"\n")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out.String(), "no longer accepts its key") {
		t.Fatalf("stale key not reported:\n%s", out)
	}

	cfg, _ = config.Load()
	if tok, _, _ := cfg.Token("bedroom", cfg.Devices["bedroom"]); tok != newKey || cfg.Default != "bedroom" {
		t.Fatalf("token = %q, default = %q", tok, cfg.Default)
	}
	if cfg.Devices["bedroom"].Location != "Living Room" {
		t.Fatalf("location lost: %q", cfg.Devices["bedroom"].Location)
	}
}

func TestSetupAlreadyDone(t *testing.T) {
	setupEnv(t)
	cfg, _ := config.Load()
	dev := &config.Device{Host: "192.168.1.100", MAC: "8CAAB5AAE3A4"}
	cfg.SetToken("bedroom", dev, newKey, false)
	cfg.Devices["bedroom"] = dev
	cfg.Save()

	n := newNetwork()
	n.keys["192.168.1.188"] = newKey
	s, out := n.setupper(t, "")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err, out)
	}
	if !strings.Contains(out.String(), "All discovered devices are set up") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	cfg, _ = config.Load()
	if cfg.Devices["bedroom"].Host != "192.168.1.188" {
		t.Fatalf("address not updated: %s", cfg.Devices["bedroom"].Host)
	}
}

func TestSetupSkip(t *testing.T) {
	setupEnv(t)
	n := newNetwork()
	s, out := n.setupper(t, "bedroom\n\n\n")
	if err := s.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if len(cfg.Devices) != 0 || !strings.Contains(out.String(), "skipped 1") {
		t.Fatalf("device saved after skip:\n%s", out)
	}
}

func TestSetupNoInput(t *testing.T) {
	setupEnv(t)
	n := newNetwork()
	s, _ := n.setupper(t, "")
	if err := s.run(context.Background()); err == nil {
		t.Fatal("expected error without input")
	}
}

func TestCheckLocation(t *testing.T) {
	setupEnv(t)
	cfg, _ := config.Load()
	cfg.Devices["a"] = &config.Device{Host: "h1", Location: "Kitchen"}
	cfg.Devices["kitchen2"] = &config.Device{Host: "h2"}
	if err := checkLocation(cfg, "b", "kitchen"); err == nil {
		t.Error("duplicate location accepted")
	}
	if err := checkLocation(cfg, "b", "Kitchen2"); err == nil {
		t.Error("location matching another device name accepted")
	}
	if err := checkLocation(cfg, "a", "Kitchen"); err != nil {
		t.Errorf("device's own location rejected: %v", err)
	}
	if err := checkLocation(cfg, "b", "Living Room"); err != nil {
		t.Errorf("new location rejected: %v", err)
	}
}
