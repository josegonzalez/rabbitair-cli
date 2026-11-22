package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/josegonzalez/rabbitair-cli/internal/config"
	"github.com/josegonzalez/rabbitair-cli/rabbitair"
)

const configUsage = `Usage: rabbitair-cli config <command> [flags]

Commands:
  add <name>       Save a device (verifies the token first)
  list             List saved devices
  remove <name>    Remove a saved device and its token
  default <name>   Set the default device
  location <name> [<location>]
                   Show or set a device's location ("" clears it)
  path             Print the config file path
`

func cmdConfig(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, configUsage)
		return flag.ErrHelp
	}
	switch args[0] {
	case "add":
		return cmdConfigAdd(ctx, args[1:], out)
	case "list", "ls":
		return cmdConfigList(args[1:], out)
	case "remove", "rm":
		return cmdConfigRemove(args[1:], out)
	case "default":
		return cmdConfigDefault(args[1:], out)
	case "location":
		return cmdConfigLocation(args[1:], out)
	case "path":
		p, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, p)
		return nil
	case "-h", "--help", "help":
		fmt.Fprint(out, configUsage)
		return nil
	}
	fmt.Fprint(os.Stderr, configUsage)
	return fmt.Errorf("unknown config command %q", args[0])
}

// parseNamed parses flags that may come before or after a single
// positional name argument.
func parseNamed(fs *flag.FlagSet, args []string) (string, error) {
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if name == "" {
		return "", errors.New("a device name is required")
	}
	return name, nil
}

func cmdConfigAdd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config add", flag.ContinueOnError)
	host := fs.String("host", "", "device IP address, hostname or device ID (required)")
	token := fs.String("token", os.Getenv("RABBITAIR_TOKEN"), "32 character hex access token (env RABBITAIR_TOKEN)")
	mac := fs.String("mac", "", "device MAC address (read from the device unless --no-verify)")
	location := fs.String("location", "", "where the device is, such as \"Living Room\" (kept when re-adding)")
	makeDefault := fs.Bool("default", false, "make this the default device")
	insecure := fs.Bool("insecure-storage", false, "store the token in the config file instead of the keychain")
	noVerify := fs.Bool("no-verify", false, "save without contacting the device")
	timeout := fs.Duration("timeout", 2*time.Second, "timeout per attempt")
	retries := fs.Int("retries", 3, "attempts per request")
	network := fs.String("network", envOr("RABBITAIR_NETWORK", "udp"), "transport to use: udp or tcp")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: rabbitair-cli config add <name> --host <host> --token <token> [flags]")
		fs.PrintDefaults()
	}

	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if *host == "" {
		return errors.New("--host is required")
	}
	if *token == "" {
		return errors.New("--token is required")
	}
	if _, err := rabbitair.ParseToken(*token); err != nil {
		return err
	}

	dev := &config.Device{Host: *host, MAC: config.NormalizeMAC(*mac), Location: strings.TrimSpace(*location)}
	if !*noVerify {
		c, err := rabbitair.NewClient(*host, *token)
		if err != nil {
			return err
		}
		c.Timeout = *timeout
		c.Retries = *retries
		c.Network = *network
		info, err := c.GetInfo(ctx)
		c.Close()
		if err != nil {
			return fmt.Errorf("could not verify token (use --no-verify to skip): %w", err)
		}
		got := config.NormalizeMAC(info.MAC)
		if dev.MAC != "" && dev.MAC != got {
			return fmt.Errorf("device MAC is %s, not %s", got, dev.MAC)
		}
		dev.MAC = got
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, n := range cfg.Names() {
		if d := cfg.Devices[n]; n != name && dev.MAC != "" && d.MAC == dev.MAC {
			return fmt.Errorf("device %s is already saved as %q", dev.MAC, n)
		}
	}
	if old, ok := cfg.Devices[name]; ok && dev.Location == "" {
		dev.Location = old.Location
	}
	if err := checkLocation(cfg, name, dev.Location); err != nil {
		return err
	}
	if _, ok := cfg.Devices[name]; ok {
		if err := cfg.Remove(name); err != nil {
			return err
		}
	}

	storage, err := cfg.SetToken(name, dev, *token, *insecure)
	if err != nil {
		return err
	}
	cfg.Devices[name] = dev
	if *makeDefault || cfg.Default == "" {
		cfg.Default = name
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved %q (%s) to %s, token in %s\n", name, dev.Host, cfg.File(), storage)
	return nil
}

func cmdConfigList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	type row struct {
		Name     string `json:"name"`
		Location string `json:"location,omitempty"`
		Host     string `json:"host"`
		MAC      string `json:"mac,omitempty"`
		Token    string `json:"token"`
		Default  bool   `json:"default"`
	}
	rows := []row{}
	for _, n := range cfg.Names() {
		d := cfg.Devices[n]
		storage := "missing"
		if _, s, err := cfg.Token(n, d); err == nil {
			storage = string(s)
		}
		rows = append(rows, row{n, d.Location, d.Host, d.MAC, storage, n == cfg.Default})
	}

	if *asJSON {
		return printJSON(out, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no saved devices")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLOCATION\tHOST\tMAC\tTOKEN\tDEFAULT")
	for _, r := range rows {
		def := ""
		if r.Default {
			def = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Location, r.Host, r.MAC, r.Token, def)
	}
	return tw.Flush()
}

func cmdConfigRemove(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config remove", flag.ContinueOnError)
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Remove(name); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %q\n", name)
	return nil
}

func cmdConfigDefault(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("config default", flag.ContinueOnError)
	name, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Devices[name]; !ok {
		return fmt.Errorf("no device named %q", name)
	}
	cfg.Default = name
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "default device is now %q\n", name)
	return nil
}

func cmdConfigLocation(args []string, out io.Writer) error {
	if len(args) == 0 || len(args) > 2 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: rabbitair-cli config location <name> [<location>]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := args[0]
	dev, ok := cfg.Devices[name]
	if !ok {
		return fmt.Errorf("no device named %q", name)
	}
	if len(args) == 1 {
		fmt.Fprintln(out, dev.Location)
		return nil
	}

	location := strings.TrimSpace(args[1])
	if err := checkLocation(cfg, name, location); err != nil {
		return err
	}
	dev.Location = location
	if err := cfg.Save(); err != nil {
		return err
	}
	if location == "" {
		fmt.Fprintf(out, "cleared the location of %q\n", name)
	} else {
		fmt.Fprintf(out, "%q is in %q\n", name, location)
	}
	return nil
}

// checkLocation rejects a location that another device already uses or
// that matches another device's name, so --device stays unambiguous.
func checkLocation(cfg *config.Config, name, location string) error {
	if location == "" {
		return nil
	}
	for _, n := range cfg.Names() {
		if n == name {
			continue
		}
		if strings.EqualFold(n, location) || strings.EqualFold(cfg.Devices[n].Location, location) {
			return fmt.Errorf("location %q is already used by %q", location, n)
		}
	}
	return nil
}
