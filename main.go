// Command rabbitair-cli discovers and controls Rabbit Air air purifiers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/josegonzalez/rabbitair-cli/internal/config"
	"github.com/josegonzalez/rabbitair-cli/rabbitair"
)

const usage = `Usage: rabbitair-cli <command> [flags]

Commands:
  setup      Find devices and save the ones that are not set up yet
  discover   Find Rabbit Air devices on the local network
  state      Show the current state of a device
  info       Show device information (firmware, MAC, uptime)
  set        Change device settings
  raw        Send a raw command and print the JSON response
  config     Save, list and remove devices

Device commands use a saved device (--device, the default device, or the
only saved device), or --host and --token. The RABBITAIR_DEVICE,
RABBITAIR_HOST and RABBITAIR_TOKEN environment variables also work. The
host may be an IP address or a device ID from "discover".

Saved tokens are kept in the OS keychain, or in the config file under
$XDG_CONFIG_HOME/rabbitair-cli when no keychain is usable.

Run "rabbitair-cli <command> -h" for command flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "error:", err)
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				fmt.Fprintln(os.Stderr, "hint: no response can mean the host is wrong or the token does not match the device")
			}
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return flag.ErrHelp
	}

	switch args[0] {
	case "discover":
		return cmdDiscover(ctx, args[1:], out)
	case "state":
		return cmdState(ctx, args[1:], out)
	case "info":
		return cmdInfo(ctx, args[1:], out)
	case "set":
		return cmdSet(ctx, args[1:], out)
	case "raw":
		return cmdRaw(ctx, args[1:], out)
	case "config":
		return cmdConfig(ctx, args[1:], out)
	case "setup":
		return cmdSetup(ctx, args[1:], out)
	case "-h", "--help", "help":
		fmt.Fprint(out, usage)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

func cmdDiscover(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 3*time.Second, "how long to listen for devices")
	ifaceName := fs.String("interface", "", "network interface to query on (default: system choice)")
	asJSON := fs.Bool("json", false, "print JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var iface *net.Interface
	if *ifaceName != "" {
		var err error
		if iface, err = net.InterfaceByName(*ifaceName); err != nil {
			return err
		}
	}

	devices, err := rabbitair.Discover(ctx, *timeout, iface)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(out, devices)
	}
	if len(devices) == 0 {
		fmt.Fprintln(out, "no devices found")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ADDRESS\tPORT\tID\tDEVICE ID")
	for _, d := range devices {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", d.Address, d.Port, d.ID, d.Name)
	}
	return tw.Flush()
}

type deviceFlags struct {
	device  *string
	host    *string
	token   *string
	timeout *time.Duration
	retries *int
	network *string
	asJSON  *bool
}

func addDeviceFlags(fs *flag.FlagSet) *deviceFlags {
	return &deviceFlags{
		device:  fs.String("device", os.Getenv("RABBITAIR_DEVICE"), "saved device name (env RABBITAIR_DEVICE)"),
		host:    fs.String("host", os.Getenv("RABBITAIR_HOST"), "device IP address, hostname or device ID (env RABBITAIR_HOST)"),
		token:   fs.String("token", os.Getenv("RABBITAIR_TOKEN"), "32 character hex access token (env RABBITAIR_TOKEN)"),
		timeout: fs.Duration("timeout", 2*time.Second, "timeout per attempt"),
		retries: fs.Int("retries", 3, "attempts per request"),
		network: fs.String("network", envOr("RABBITAIR_NETWORK", "udp"), "transport to use: udp or tcp (env RABBITAIR_NETWORK)"),
		asJSON:  fs.Bool("json", false, "print JSON output"),
	}
}

// resolve fills in the host and token from saved devices when they are
// not given on the command line or in the environment.
func (f *deviceFlags) resolve() (host, token string, err error) {
	host, token = *f.host, *f.token
	if host != "" && token != "" && *f.device == "" {
		return host, token, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return "", "", err
	}

	var (
		name string
		dev  *config.Device
		ok   bool
	)
	switch {
	case *f.device != "":
		if name, dev, ok = cfg.Lookup(*f.device); !ok {
			return "", "", fmt.Errorf("no saved device named %q (see \"config list\")", *f.device)
		}
	case host != "":
		name, dev, ok = cfg.Lookup(host)
	case cfg.Default != "":
		if name, dev, ok = cfg.Lookup(cfg.Default); !ok {
			return "", "", fmt.Errorf("default device %q is not saved", cfg.Default)
		}
	case len(cfg.Devices) == 1:
		name = cfg.Names()[0]
		dev, ok = cfg.Devices[name], true
	}

	if ok {
		if host == "" {
			host = dev.Host
		}
		if token == "" {
			if token, _, err = cfg.Token(name, dev); err != nil {
				return "", "", err
			}
		}
	}
	if host == "" {
		return "", "", errors.New("no device given: pass --device or --host, or save one with \"config add\"")
	}
	if token == "" {
		return "", "", errors.New("--token is required (or save the device with \"config add\")")
	}
	return host, token, nil
}

func (f *deviceFlags) client() (*rabbitair.Client, error) {
	host, token, err := f.resolve()
	if err != nil {
		return nil, err
	}
	return f.newClient(host, token)
}

func (f *deviceFlags) newClient(host, token string) (*rabbitair.Client, error) {
	c, err := rabbitair.NewClient(host, token)
	if err != nil {
		return nil, err
	}
	c.Timeout = *f.timeout
	c.Retries = *f.retries
	c.Network = *f.network
	return c, nil
}

func cmdState(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := df.client()
	if err != nil {
		return err
	}
	defer c.Close()

	s, err := c.GetState(ctx)
	if err != nil {
		return err
	}
	if *df.asJSON {
		return printJSON(out, s.Raw)
	}
	printState(out, s)
	return nil
}

func cmdInfo(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := df.client()
	if err != nil {
		return err
	}
	defer c.Close()

	i, err := c.GetInfo(ctx)
	if err != nil {
		return err
	}
	if *df.asJSON {
		return printJSON(out, i.Raw)
	}

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	row := func(k string, v any) { fmt.Fprintf(tw, "%s:\t%v\n", k, v) }
	row("Device ID", i.Name)
	row("MAC", i.MAC)
	row("Wi-Fi firmware", i.MCU)
	row("Wi-Fi build", i.Build)
	if i.MainFirmware != nil {
		row("Main firmware", *i.MainFirmware)
	}
	if i.Time != nil {
		row("Time", *i.Time)
	}
	row("Uptime", seconds(i.Uptime))
	row("Motor uptime", seconds(i.MotorUptime))
	row("Wi-Fi uptime", seconds(i.WifiUptime))
	if i.InternetUptime != nil {
		row("Internet uptime", seconds(*i.InternetUptime))
	}
	if i.CloudUptime != nil {
		row("Cloud uptime", seconds(*i.CloudUptime))
	}
	if r := i.RSSI; r != nil && r.Cur != nil {
		row("RSSI", fmt.Sprintf("%d dBm (min %s, max %s, avg %s)", *r.Cur, intp(r.Min), intp(r.Max), intp(r.Avg)))
	}
	return tw.Flush()
}

func cmdSet(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	df := addDeviceFlags(fs)

	u := &rabbitair.Update{}
	boolFlag(fs, &u.Power, "power", "turn the device on or off (on|off)")
	enumFlag(fs, &u.Mode, rabbitair.Modes, "mode", "mode of operation")
	enumFlag(fs, &u.Speed, rabbitair.Speeds, "speed", "fan speed")
	enumFlag(fs, &u.Sensitivity, rabbitair.Sensitivities, "sensitivity", "sensor sensitivity")
	boolFlag(fs, &u.Ionizer, "ionizer", "negative ion generator (on|off)")
	enumFlag(fs, &u.Moodlight, rabbitair.Moodlights, "moodlight", "mood light mode")
	enumFlag(fs, &u.Lights, rabbitair.Lights, "lights", "all lights")
	boolFlag(fs, &u.LightSensorCtl, "light-sensor", "light sensor control (on|off)")
	boolFlag(fs, &u.FilterCtl, "filter-alert", "filter replacement notification (on|off)")
	boolFlag(fs, &u.Buzzer, "buzzer", "buzzer sound (on|off)")
	boolFlag(fs, &u.ChildLock, "child-lock", "child lock (on|off)")
	enumFlag(fs, &u.TimerMode, rabbitair.TimerModes, "timer-mode", "timer mode")
	intFlag(fs, &u.Timer, "timer", "minutes until the unit turns off (0-1440)")
	intFlag(fs, &u.FilterLife, "filter-life", "remaining filter lifetime in minutes (0-525600)")
	intFlag(fs, &u.FilterTimer, "filter-timer", "nominal filter lifetime in minutes (0-525600)")
	boolFlag(fs, &u.FilterCleaning, "filter-cleaning", "filter cleaning required flag (on|off)")
	boolFlag(fs, &u.FilterReplacement, "filter-replacement", "filter replacement required flag (on|off)")
	fs.Func("schedule", "24 character UTC schedule, one speed per hour (0-5 or A for auto)", func(s string) error {
		u.Schedule = &s
		return nil
	})
	fs.Func("color", "mood light palette as 9 comma separated values (0-40)", func(s string) error {
		for _, p := range strings.Split(s, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				return err
			}
			u.Color = append(u.Color, v)
		}
		return nil
	})

	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := u.Validate(); err != nil {
		return err
	}
	c, err := df.client()
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.SetState(ctx, u); err != nil {
		return err
	}
	s, err := c.GetState(ctx)
	if err != nil {
		return err
	}
	if *df.asJSON {
		return printJSON(out, s.Raw)
	}
	printState(out, s)
	return nil
}

func cmdRaw(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("raw", flag.ContinueOnError)
	df := addDeviceFlags(fs)
	cmd := fs.Int("cmd", rabbitair.CmdState, "command number (4 state, 9 timestamp, 255 info)")
	data := fs.String("data", "", "JSON object to send as the data field")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var payload any
	if *data != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(*data), &m); err != nil {
			return fmt.Errorf("--data: %w", err)
		}
		payload = m
	}
	c, err := df.client()
	if err != nil {
		return err
	}
	defer c.Close()

	resp, err := c.Command(ctx, *cmd, payload)
	if err != nil {
		return err
	}
	return printJSON(out, resp)
}

func printState(out io.Writer, s *rabbitair.State) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	row := func(k string, v any) { fmt.Fprintf(tw, "%s:\t%v\n", k, v) }
	enum := func(k string, e rabbitair.Enum, v *int) {
		if v != nil {
			row(k, strings.ToLower(rabbitair.EnumName(e, *v)))
		}
	}
	flag := func(k string, v *bool) {
		if v != nil {
			row(k, onOff(*v))
		}
	}

	if s.Model != nil {
		row("Model", s.ModelName())
	}
	if len(s.Firmware) > 0 {
		parts := make([]string, len(s.Firmware))
		for i, v := range s.Firmware {
			parts[i] = strconv.Itoa(v)
		}
		row("Main firmware", strings.Join(parts, "."))
	}
	if s.WifiFirmware != nil {
		row("Wi-Fi firmware", *s.WifiFirmware)
	}
	flag("Power", s.Power)
	enum("Mode", rabbitair.Modes, s.Mode)
	enum("Speed", rabbitair.Speeds, s.Speed)
	if s.Quality != nil {
		row("Air quality", strings.ToLower(s.QualityName()))
	}
	enum("Sensitivity", rabbitair.Sensitivities, s.Sensitivity)
	flag("Ionizer", s.Ionizer)
	enum("Mood light", rabbitair.Moodlights, s.Moodlight)
	enum("Lights", rabbitair.Lights, s.AllLightOff)
	if s.Idle != nil {
		row("Idle", onOff(*s.Idle != 0))
	}
	flag("Sleep", s.Sleep)
	enum("Filter type", rabbitair.FilterTypes, s.FilterType)
	if s.FilterLife != nil {
		row("Filter life", fmt.Sprintf("%d min", *s.FilterLife))
	}
	if s.FilterTimer != nil {
		row("Filter timer", fmt.Sprintf("%d min", *s.FilterTimer))
	}
	flag("Filter cleaning", s.FilterCleaning)
	flag("Filter replacement", s.FilterReplacement)
	flag("Filter alert", s.FilterCtl)
	if s.ParticulateSensor != nil {
		row("Particulate sensor", *s.ParticulateSensor)
	}
	if len(s.PMSensor) > 0 {
		row("PM sensor", fmt.Sprint(s.PMSensor))
	}
	enum("Gas sensor", rabbitair.Gases, s.Gas)
	flag("Light sensor", s.LightSensor)
	flag("Light sensor control", s.LightSensorCtl)
	flag("Buzzer", s.Buzzer)
	flag("Child lock", s.Lock)
	flag("Panel open", s.Open)
	enum("Timer mode", rabbitair.TimerModes, s.TimerMode)
	if s.Timer != nil {
		row("Timer", fmt.Sprintf("%d min", *s.Timer))
	}
	if s.Schedule != nil {
		row("Schedule", *s.Schedule)
	}
	if s.RSSI != nil {
		row("RSSI", fmt.Sprintf("%d dBm", *s.RSSI))
	}
	enum("Error", rabbitair.Errors, s.Error)
	tw.Flush()
}

func boolFlag(fs *flag.FlagSet, dst **bool, name, usage string) {
	fs.Func(name, usage, func(s string) error {
		var v bool
		switch strings.ToLower(s) {
		case "on", "true", "yes", "1":
			v = true
		case "off", "false", "no", "0":
			v = false
		default:
			return fmt.Errorf("expected on or off, got %q", s)
		}
		*dst = &v
		return nil
	})
}

func enumFlag(fs *flag.FlagSet, dst **int, e rabbitair.Enum, name, usage string) {
	fs.Func(name, fmt.Sprintf("%s (%s)", usage, strings.Join(e.Names(), "|")), func(s string) error {
		v, err := rabbitair.ParseEnum(e, name, s)
		if err != nil {
			return err
		}
		*dst = &v
		return nil
	})
}

func intFlag(fs *flag.FlagSet, dst **int, name, usage string) {
	fs.Func(name, usage, func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return err
		}
		*dst = &v
		return nil
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func intp(v *int) string {
	if v == nil {
		return "?"
	}
	return strconv.Itoa(*v)
}

func seconds(s int64) string {
	return (time.Duration(s) * time.Second).String()
}

func printJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
