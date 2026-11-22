package rabbitair

import (
	"context"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

// ServiceType is the mDNS service Rabbit Air devices advertise.
const ServiceType = "_rabbitair._udp"

// Device is a Rabbit Air device found on the network.
type Device struct {
	// Name is the device ID (the mDNS instance name).
	Name string `json:"name"`
	// Host is the mDNS hostname.
	Host string `json:"host"`
	// Address is the IPv4 address.
	Address string `json:"address"`
	Port    int    `json:"port"`
	// ID is the "id" TXT record, which appears to be the MAC address.
	ID string `json:"id,omitempty"`
	// TXT holds all TXT records.
	TXT map[string]string `json:"txt,omitempty"`
}

// Discover browses mDNS for Rabbit Air devices for the given duration.
func Discover(ctx context.Context, timeout time.Duration, iface *net.Interface) ([]Device, error) {
	entries := make(chan *mdns.ServiceEntry, 32)
	found := map[string]Device{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		for e := range entries {
			if e.AddrV4 == nil || !strings.Contains(e.Name, ServiceType+".") {
				continue
			}
			d := Device{
				Name:    strings.TrimSuffix(e.Name, "."+ServiceType+".local."),
				Host:    strings.TrimSuffix(e.Host, "."),
				Address: e.AddrV4.String(),
				Port:    e.Port,
				TXT:     map[string]string{},
			}
			for _, f := range e.InfoFields {
				k, v, _ := strings.Cut(f, "=")
				d.TXT[k] = v
			}
			d.ID = d.TXT["id"]
			key := d.ID
			if key == "" {
				key = d.Name
			}
			found[key] = d
		}
	}()

	// A single query can miss replies, so send several across the
	// window, alternating unicast and multicast responses, and merge them.
	const rounds = 3
	var err error
	for i := range rounds {
		params := mdns.DefaultParams(ServiceType)
		params.Domain = "local"
		params.Timeout = max(timeout/rounds, 200*time.Millisecond)
		params.Entries = entries
		params.DisableIPv6 = true
		params.Interface = iface
		params.WantUnicastResponse = i%2 == 1
		params.Logger = log.New(io.Discard, "", 0)
		if err = mdns.QueryContext(ctx, params); err != nil || ctx.Err() != nil {
			break
		}
	}
	close(entries)
	<-done
	if err != nil {
		return nil, err
	}

	devices := make([]Device, 0, len(found))
	for _, d := range found {
		devices = append(devices, d)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Address < devices[j].Address })
	return devices, nil
}
