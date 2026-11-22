package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/josegonzalez/rabbitair-cli/internal/config"
	"github.com/josegonzalez/rabbitair-cli/rabbitair"
)

const keyInstructions = `To find the user key in the Rabbit Air app:

  Rabbit Air 2 app:
    1. Tap the purifier to open its control page.
    2. Open the three-dot menu in the top right and select "Rename".
    3. Tap the device name to reveal the Thing ID and User key.

  Older Rabbit Air app:
    1. Tap the purifier to open its control page, then tap "Edit".
    2. Quickly tap "Serial Number" several times until two more lines
       appear: the device ID, then the user key.
    3. If you see "Tap for setup user key", tap it and follow the prompts.
`

const repairInstructions = `The purifier did not accept that key.

If the Thing ID and user key shown in the app match the ones above, the key
saved on the purifier is probably out of date. Re-pair it in the app:

  1. Remove the purifier from the app.
  2. Add it again and finish the Wi-Fi setup.
  3. Open the Thing ID and user key again. Both will have changed.

If the app says the device is not supported, it may be a first generation
MinusA2, which Home Assistant lists as not supported.
`

// setupDevice is a discovered device and its saved entry, if any.
type setupDevice struct {
	found rabbitair.Device
	mac   string
	name  string
	saved *config.Device
}

type setupper struct {
	in         *bufio.Reader
	out        io.Writer
	readSecret func(ctx context.Context, prompt string) (string, error)
	discover   func(ctx context.Context) ([]rabbitair.Device, error)
	verify     func(ctx context.Context, host, token string) (*rabbitair.Info, error)
	cfg        *config.Config
	insecure   bool
	checkSaved bool
}

func cmdSetup(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	discoverTimeout := fs.Duration("discover-timeout", 3*time.Second, "how long to listen for devices")
	timeout := fs.Duration("timeout", 2*time.Second, "timeout per request attempt")
	retries := fs.Int("retries", 3, "attempts per request")
	network := fs.String("network", envOr("RABBITAIR_NETWORK", "udp"), "transport to use: udp or tcp")
	insecure := fs.Bool("insecure-storage", false, "store tokens in the config file instead of the keychain")
	skipSaved := fs.Bool("skip-saved", false, "do not check that saved devices still accept their keys")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	in := bufio.NewReader(os.Stdin)
	s := &setupper{
		in:  in,
		out: out,
		discover: func(ctx context.Context) ([]rabbitair.Device, error) {
			return rabbitair.Discover(ctx, *discoverTimeout, nil)
		},
		verify: func(ctx context.Context, host, token string) (*rabbitair.Info, error) {
			c, err := rabbitair.NewClient(host, token)
			if err != nil {
				return nil, err
			}
			defer c.Close()
			c.Timeout = *timeout
			c.Retries = *retries
			c.Network = *network
			return c.GetInfo(ctx)
		},
		cfg:        cfg,
		insecure:   *insecure,
		checkSaved: !*skipSaved,
	}
	s.readSecret = func(ctx context.Context, prompt string) (string, error) {
		return readSecret(ctx, s, prompt)
	}
	return s.run(ctx)
}

func (s *setupper) run(ctx context.Context) error {
	fmt.Fprintln(s.out, "Searching for Rabbit Air devices...")
	found, err := s.discover(ctx)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(s.out, "No devices found. Make sure this computer is on the same network as the purifiers.")
		return nil
	}

	var pending []*setupDevice
	seen := map[string]bool{}
	for _, f := range found {
		d := s.match(f)
		if d.saved != nil {
			seen[d.name] = true
		}

		switch {
		case d.saved == nil:
			fmt.Fprintf(s.out, "  %s  %s  not saved\n", f.Address, d.mac)
			pending = append(pending, d)
		case !s.checkSaved:
			fmt.Fprintf(s.out, "  %s  %s  saved as %s\n", f.Address, d.mac, label(d))
			s.updateHost(d)
		default:
			if s.checkKey(ctx, d) {
				fmt.Fprintf(s.out, "  %s  %s  saved as %s, key works\n", f.Address, d.mac, label(d))
				s.updateHost(d)
			} else {
				fmt.Fprintf(s.out, "  %s  %s  saved as %s, saved key did not work\n", f.Address, d.mac, label(d))
				pending = append(pending, d)
			}
		}
	}

	for _, n := range s.cfg.Names() {
		if !seen[n] {
			fmt.Fprintf(s.out, "  saved device %q (%s) was not found on the network\n", n, s.cfg.Devices[n].Host)
		}
	}

	if len(pending) == 0 {
		fmt.Fprintln(s.out, "\nAll discovered devices are set up.")
		return nil
	}

	fmt.Fprintf(s.out, "\n%s", keyInstructions)
	var done, skipped int
	for _, d := range pending {
		ok, err := s.setupOne(ctx, d)
		if err != nil {
			return err
		}
		if ok {
			done++
		} else {
			skipped++
		}
	}
	fmt.Fprintf(s.out, "\nSet up %d device(s), skipped %d. Config: %s\n", done, skipped, s.cfg.File())
	return nil
}

// match pairs a discovered device with a saved one by MAC, then by address.
func (s *setupper) match(f rabbitair.Device) *setupDevice {
	d := &setupDevice{found: f, mac: config.NormalizeMAC(f.ID)}
	if d.mac != "" {
		if name, saved, ok := s.cfg.Lookup(d.mac); ok {
			d.name, d.saved = name, saved
			return d
		}
	}
	if name, saved, ok := s.cfg.Lookup(f.Address); ok && (saved.MAC == "" || d.mac == "") {
		d.name, d.saved = name, saved
	}
	return d
}

func (s *setupper) checkKey(ctx context.Context, d *setupDevice) bool {
	token, _, err := s.cfg.Token(d.name, d.saved)
	if err != nil {
		return false
	}
	_, err = s.verify(ctx, d.found.Address, token)
	return err == nil
}

// updateHost saves a new address for a device whose IP changed.
func (s *setupper) updateHost(d *setupDevice) {
	if d.saved.Host == d.found.Address || strings.HasSuffix(d.saved.Host, ".local") {
		return
	}
	fmt.Fprintf(s.out, "    address changed from %s, updating\n", d.saved.Host)
	d.saved.Host = d.found.Address
	if err := s.cfg.Save(); err != nil {
		fmt.Fprintf(s.out, "    could not save: %v\n", err)
	}
}

func (s *setupper) setupOne(ctx context.Context, d *setupDevice) (bool, error) {
	fmt.Fprintf(s.out, "\n--- %s (MAC %s)\n", d.found.Address, d.mac)
	if d.saved != nil {
		fmt.Fprintf(s.out, "Saved as %q, but the purifier no longer accepts its key.\n", d.name)
	}

	name, location := d.name, ""
	if d.saved != nil {
		location = d.saved.Location
	}
	if name == "" {
		var err error
		if name, err = s.askName(ctx, d); err != nil || name == "" {
			return false, err
		}
		if location, err = s.askLocation(ctx, name); err != nil {
			return false, err
		}
	}

	for {
		fmt.Fprintf(s.out, "The app should show Thing ID %s for this purifier.\n", d.found.Name)
		token, err := s.readSecret(ctx, "User key (leave empty to skip): ")
		if err != nil {
			return false, err
		}
		token = strings.TrimSpace(token)
		if token == "" {
			fmt.Fprintln(s.out, "Skipped.")
			return false, nil
		}
		if _, err := rabbitair.ParseToken(token); err != nil {
			fmt.Fprintf(s.out, "%v. Try again.\n", err)
			continue
		}

		fmt.Fprintln(s.out, "Checking the key...")
		info, err := s.verify(ctx, d.found.Address, token)
		if err == nil {
			if got := config.NormalizeMAC(info.MAC); d.mac != "" && got != "" && got != d.mac {
				return false, fmt.Errorf("device at %s reported MAC %s, expected %s", d.found.Address, got, d.mac)
			}
			return true, s.save(name, location, d, token)
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		fmt.Fprintf(s.out, "\n%s\n", repairInstructions)
		choice, err := s.ask(ctx, "[r]etry with a new key after re-pairing, or [s]kip? [r]: ")
		if err != nil {
			return false, err
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(choice)), "s") {
			fmt.Fprintln(s.out, "Skipped.")
			return false, nil
		}
		s.refresh(ctx, d)
	}
}

// refresh rediscovers a device after re-pairing, which changes its Thing
// ID and may change its address.
func (s *setupper) refresh(ctx context.Context, d *setupDevice) {
	fmt.Fprintln(s.out, "Looking for the purifier again...")
	found, err := s.discover(ctx)
	if err != nil {
		return
	}
	for _, f := range found {
		if (d.mac != "" && config.NormalizeMAC(f.ID) == d.mac) || (d.mac == "" && f.Address == d.found.Address) {
			if f.Name != d.found.Name {
				fmt.Fprintf(s.out, "Its Thing ID changed to %s.\n", f.Name)
			}
			if f.Address != d.found.Address {
				fmt.Fprintf(s.out, "Its address changed to %s.\n", f.Address)
			}
			d.found = f
			return
		}
	}
	fmt.Fprintln(s.out, "Not found yet; it may still be reconnecting. Trying the last known address.")
}

func (s *setupper) askName(ctx context.Context, d *setupDevice) (string, error) {
	def := "purifier"
	if len(d.mac) >= 6 {
		def = "purifier-" + strings.ToLower(d.mac[len(d.mac)-6:])
	}
	for {
		name, err := s.ask(ctx, fmt.Sprintf("Name for this purifier [%s]: ", def))
		if err != nil {
			return "", err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = def
		}
		if strings.ContainsAny(name, " \t\"'") {
			fmt.Fprintln(s.out, "Names cannot contain spaces or quotes.")
			continue
		}
		if _, ok := s.cfg.Devices[name]; ok {
			fmt.Fprintf(s.out, "%q is already used by another device.\n", name)
			continue
		}
		return name, nil
	}
}

func (s *setupper) askLocation(ctx context.Context, name string) (string, error) {
	for {
		location, err := s.ask(ctx, "Location, such as \"Living Room\" (optional): ")
		if err != nil {
			return "", err
		}
		location = strings.TrimSpace(location)
		if err := checkLocation(s.cfg, name, location); err != nil {
			fmt.Fprintf(s.out, "%v.\n", err)
			continue
		}
		return location, nil
	}
}

func (s *setupper) save(name, location string, d *setupDevice, token string) error {
	if _, ok := s.cfg.Devices[name]; ok {
		if err := s.cfg.Remove(name); err != nil {
			return err
		}
	}
	dev := &config.Device{Host: d.found.Address, MAC: d.mac, Location: location}
	storage, err := s.cfg.SetToken(name, dev, token, s.insecure)
	if err != nil {
		return err
	}
	s.cfg.Devices[name] = dev
	if s.cfg.Default == "" {
		s.cfg.Default = name
	}
	if err := s.cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Saved %q, token in %s.\n", name, storage)
	return nil
}

// ask prints a prompt and reads a line, returning early on cancellation.
func (s *setupper) ask(ctx context.Context, prompt string) (string, error) {
	fmt.Fprint(s.out, prompt)
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := s.in.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprintln(s.out)
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil && !(errors.Is(r.err, io.EOF) && r.line != "") {
			if errors.Is(r.err, io.EOF) {
				return "", errors.New("setup needs answers on standard input")
			}
			return "", r.err
		}
		return strings.TrimRight(r.line, "\r\n"), nil
	}
}

// readSecret reads without echo when stdin is a terminal.
func readSecret(ctx context.Context, s *setupper, prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return s.ask(ctx, prompt)
	}
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}

	fmt.Fprint(s.out, prompt)
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := term.ReadPassword(fd)
		ch <- result{b, err}
	}()
	select {
	case <-ctx.Done():
		term.Restore(fd, state)
		fmt.Fprintln(s.out)
		return "", ctx.Err()
	case r := <-ch:
		fmt.Fprintln(s.out)
		return string(r.b), r.err
	}
}

// label describes a saved device by name and location.
func label(d *setupDevice) string {
	if d.saved != nil && d.saved.Location != "" {
		return fmt.Sprintf("%q (%s)", d.name, d.saved.Location)
	}
	return fmt.Sprintf("%q", d.name)
}
