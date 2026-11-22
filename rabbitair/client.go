// Package rabbitair implements the Rabbit Air local UDP protocol.
//
// The protocol is based on the Apache-2.0 licensed python-rabbitair library
// (https://github.com/rabbit-air/python-rabbitair).
package rabbitair

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"time"
)

// DefaultPort is the UDP port Rabbit Air devices listen on.
const DefaultPort = 9009

// Command identifiers.
const (
	CmdState     = 4
	CmdTimestamp = 9
	CmdInfo      = 255
)

// ProtocolError is returned when the device answers with an error.
type ProtocolError struct {
	Raw json.RawMessage
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("device returned error: %s", string(e.Raw))
}

type request struct {
	ID   uint32 `json:"id"`
	Cmd  int    `json:"cmd"`
	Data any    `json:"data,omitempty"`
	TS   *int64 `json:"ts,omitempty"`
}

type response struct {
	ID    *uint32         `json:"id"`
	Data  json.RawMessage `json:"data"`
	Error json.RawMessage `json:"error"`
}

// Client talks to a single Rabbit Air device.
type Client struct {
	// Timeout is how long to wait for each attempt.
	Timeout time.Duration
	// Retries is the number of attempts per request.
	Retries int
	// Network is "udp" (default) or "tcp". TCP messages are prefixed
	// with a 2-byte little-endian length.
	Network string

	host string
	key  []byte

	mu     sync.Mutex
	conn   net.Conn
	nextID uint32
	tsBase int64
	tsAt   time.Time
	synced bool
}

// NewClient returns a client for host, which may be an IP address, a
// hostname, a device ID (".local" is appended) and may include a port.
// token is the 32 character hex access token; pass "" for none.
func NewClient(host, token string) (*Client, error) {
	c := &Client{
		Timeout: 2 * time.Second,
		Retries: 3,
		Network: "udp",
		host:    normalizeHost(host),
	}
	if token != "" {
		key, err := ParseToken(token)
		if err != nil {
			return nil, err
		}
		c.key = key
	}

	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	c.nextID = binary.BigEndian.Uint32(b[:]) % 0x1000000
	return c, nil
}

func normalizeHost(host string) string {
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		h, port = host, fmt.Sprint(DefaultPort)
	}
	if net.ParseIP(h) == nil && !strings.Contains(h, ".") && h != "localhost" {
		h += ".local"
	}
	return net.JoinHostPort(h, port)
}

// Close releases the underlying socket.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset()
	return nil
}

func (c *Client) reset() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.synced = false
}

// Command sends a raw command and returns the "data" field of the response.
func (c *Client) Command(ctx context.Context, cmd int, data any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		var d net.Dialer
		network := c.Network
		if network == "" {
			network = "udp"
		}
		if network != "udp" && network != "tcp" {
			return nil, fmt.Errorf("unsupported network %q", network)
		}
		conn, err := d.DialContext(ctx, network, c.host)
		if err != nil {
			return nil, err
		}
		c.conn = conn
	}

	req := request{Cmd: cmd, Data: data}
	if c.key != nil {
		if !c.synced {
			resp, err := c.exchange(ctx, request{ID: c.id(), Cmd: CmdTimestamp})
			if err != nil {
				c.reset()
				return nil, fmt.Errorf("timestamp sync: %w", err)
			}
			var ts struct {
				TS int64 `json:"ts"`
			}
			if err := json.Unmarshal(resp, &ts); err != nil {
				c.reset()
				return nil, fmt.Errorf("timestamp sync: %w", err)
			}
			c.tsBase, c.tsAt, c.synced = ts.TS, time.Now(), true
		}
		ts := c.tsBase + int64(math.Round(time.Since(c.tsAt).Seconds()))
		req.TS = &ts
	}
	req.ID = c.id()

	resp, err := c.exchange(ctx, req)
	if err != nil {
		var pe *ProtocolError
		if !errors.As(err, &pe) {
			c.reset()
		}
		return nil, err
	}
	return resp, nil
}

func (c *Client) id() uint32 {
	id := c.nextID
	c.nextID++
	return id
}

func (c *Client) exchange(ctx context.Context, req request) (json.RawMessage, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if c.key != nil {
		if payload, err = encrypt(c.key, payload); err != nil {
			return nil, err
		}
	}

	retries := max(c.Retries, 1)
	var lastErr error
	for range retries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := c.writeMsg(payload); err != nil {
			return nil, err
		}
		resp, err := c.await(ctx, req.ID)
		if err == nil {
			return resp, nil
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no response from %s after %d attempts: %w", c.host, retries, lastErr)
}

func (c *Client) await(ctx context.Context, id uint32) (json.RawMessage, error) {
	deadline := time.Now().Add(c.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}

	for {
		msg, err := c.readMsg()
		if err != nil {
			return nil, err
		}
		if c.key != nil {
			if msg, err = decrypt(c.key, msg); err != nil {
				continue
			}
		}

		var resp response
		if err := json.Unmarshal(msg, &resp); err != nil || resp.ID == nil || *resp.ID != id {
			continue
		}
		if isTruthy(resp.Error) {
			return nil, &ProtocolError{Raw: resp.Error}
		}
		return resp.Data, nil
	}
}

func (c *Client) isTCP() bool { return c.Network == "tcp" }

func (c *Client) writeMsg(payload []byte) error {
	if c.isTCP() {
		if len(payload) > math.MaxUint16 {
			return errors.New("message too large")
		}
		payload = append(binary.LittleEndian.AppendUint16(nil, uint16(len(payload))), payload...)
	}
	_, err := c.conn.Write(payload)
	return err
}

func (c *Client) readMsg() ([]byte, error) {
	if c.isTCP() {
		var hdr [2]byte
		if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
			return nil, err
		}
		msg := make([]byte, binary.LittleEndian.Uint16(hdr[:]))
		if _, err := io.ReadFull(c.conn, msg); err != nil {
			return nil, err
		}
		return msg, nil
	}
	buf := make([]byte, 2048)
	n, err := c.conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func isTruthy(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false", "0", `""`, "[]", "{}":
		return false
	}
	return true
}

// GetState returns the current device state.
func (c *Client) GetState(ctx context.Context) (*State, error) {
	data, err := c.Command(ctx, CmdState, nil)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	s.Raw = data
	return &s, nil
}

// GetInfo returns device information.
func (c *Client) GetInfo(ctx context.Context) (*Info, error) {
	data, err := c.Command(ctx, CmdInfo, nil)
	if err != nil {
		return nil, err
	}
	var i Info
	if err := json.Unmarshal(data, &i); err != nil {
		return nil, fmt.Errorf("decode info: %w", err)
	}
	i.Raw = data
	return &i, nil
}

// SetState changes device settings. Only non-nil fields are sent.
func (c *Client) SetState(ctx context.Context, u *Update) error {
	if err := u.Validate(); err != nil {
		return err
	}
	_, err := c.Command(ctx, CmdState, u)
	return err
}
