package rabbitair

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestEncryptRoundTrip(t *testing.T) {
	key, err := ParseToken(testToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range [][]byte{{}, []byte("x"), bytes.Repeat([]byte("a"), 16), bytes.Repeat([]byte("b"), 37)} {
		enc, err := encrypt(key, msg)
		if err != nil {
			t.Fatal(err)
		}
		if len(enc)%16 != 0 || len(enc) < 32 {
			t.Fatalf("bad ciphertext length %d", len(enc))
		}
		dec, err := decrypt(key, enc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(dec, msg) {
			t.Fatalf("got %q, want %q", dec, msg)
		}
	}
}

func TestParseToken(t *testing.T) {
	for _, tok := range []string{"", "abc", "zz23456789abcdef0123456789abcdef", testToken + "00"} {
		if _, err := ParseToken(tok); err == nil {
			t.Errorf("ParseToken(%q) succeeded", tok)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"192.168.1.188":      "192.168.1.188:9009",
		"192.168.1.188:1234": "192.168.1.188:1234",
		"abc123":             "abc123.local:9009",
		"abc123.local":       "abc123.local:9009",
		"localhost":          "localhost:9009",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeDevice emulates a Rabbit Air device over UDP.
type fakeDevice struct {
	t    *testing.T
	key  []byte
	conn *net.UDPConn

	mu       sync.Mutex
	requests []map[string]any
	state    map[string]any
	drop     int
}

func newFakeDevice(t *testing.T, drop int) *fakeDevice {
	key, _ := ParseToken(testToken)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDevice{t: t, key: key, conn: conn, drop: drop, state: map[string]any{
		"model": 1, "power": true, "mode": 0, "speed": 3, "quality": 1, "firmware": []int{1, 2},
	}}
	t.Cleanup(func() { conn.Close() })
	go d.serve()
	return d
}

func (d *fakeDevice) addr() string { return d.conn.LocalAddr().String() }

func (d *fakeDevice) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		plain, err := decrypt(d.key, buf[:n])
		if err != nil {
			continue
		}
		var req map[string]any
		if err := json.Unmarshal(plain, &req); err != nil {
			continue
		}

		d.mu.Lock()
		d.requests = append(d.requests, req)
		if d.drop > 0 {
			d.drop--
			d.mu.Unlock()
			continue
		}
		resp := map[string]any{"id": req["id"]}
		switch int(req["cmd"].(float64)) {
		case CmdTimestamp:
			resp["data"] = map[string]any{"ts": 1000}
		case CmdInfo:
			resp["data"] = map[string]any{"name": "dev", "mac": "AA:BB", "mcu": "1.0", "build": "b", "uptime": 60, "mup": 1, "wup": 2}
		case CmdState:
			if data, ok := req["data"].(map[string]any); ok {
				if _, bad := data["bogus"]; bad {
					resp["error"] = 1
				}
				for k, v := range data {
					d.state[k] = v
				}
			}
			resp["data"] = d.state
		}
		d.mu.Unlock()

		// Send an unrelated packet first to check it is ignored.
		noise, _ := encrypt(d.key, []byte(`{"id":-1}`))
		d.conn.WriteToUDP(noise, from)

		out, _ := json.Marshal(resp)
		enc, _ := encrypt(d.key, out)
		d.conn.WriteToUDP(enc, from)
	}
}

func newTestClient(t *testing.T, d *fakeDevice) *Client {
	c, err := NewClient(d.addr(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.Timeout = 200 * time.Millisecond
	t.Cleanup(func() { c.Close() })
	return c
}

func TestGetState(t *testing.T) {
	d := newFakeDevice(t, 0)
	c := newTestClient(t, d)

	s, err := c.GetState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ModelName() != "MinusA2" || s.Power == nil || !*s.Power || EnumName(Speeds, *s.Speed) != "Medium" {
		t.Fatalf("unexpected state %s", s.Raw)
	}
	if s.QualityName() != "Low" {
		t.Fatalf("quality = %s", s.QualityName())
	}

	if _, err := c.GetState(context.Background()); err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) != 3 {
		t.Fatalf("got %d requests, want 3 (sync once)", len(d.requests))
	}
	if d.requests[0]["cmd"].(float64) != CmdTimestamp {
		t.Fatalf("first request should sync timestamp: %v", d.requests[0])
	}
	for _, r := range d.requests[1:] {
		if r["ts"].(float64) < 1000 {
			t.Fatalf("missing ts: %v", r)
		}
	}
}

func TestSetState(t *testing.T) {
	d := newFakeDevice(t, 0)
	c := newTestClient(t, d)

	off, speed := false, 5
	if err := c.SetState(context.Background(), &Update{Power: &off, Speed: &speed}); err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	data := d.requests[len(d.requests)-1]["data"].(map[string]any)
	if data["power"] != false || data["speed"].(float64) != 5 || len(data) != 2 {
		t.Fatalf("unexpected data %v", data)
	}
}

func TestGetInfo(t *testing.T) {
	d := newFakeDevice(t, 0)
	c := newTestClient(t, d)
	i, err := c.GetInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if i.Name != "dev" || i.MAC != "AA:BB" || i.Uptime != 60 {
		t.Fatalf("unexpected info %s", i.Raw)
	}
}

func TestProtocolError(t *testing.T) {
	d := newFakeDevice(t, 0)
	c := newTestClient(t, d)
	_, err := c.Command(context.Background(), CmdState, map[string]any{"bogus": 1})
	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("got %v, want ProtocolError", err)
	}
}

func TestRetry(t *testing.T) {
	d := newFakeDevice(t, 2)
	c := newTestClient(t, d)
	if _, err := c.GetState(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTimeout(t *testing.T) {
	d := newFakeDevice(t, 100)
	c := newTestClient(t, d)
	c.Timeout = 50 * time.Millisecond
	c.Retries = 2
	if _, err := c.GetState(context.Background()); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestUpdateValidate(t *testing.T) {
	zero, big, bad := 0, 2000, "12345"
	cases := []*Update{
		{},
		{Speed: &zero},
		{Timer: &big},
		{Schedule: &bad},
		{Color: []int{1, 2}},
	}
	for _, u := range cases {
		if err := u.Validate(); err == nil {
			b, _ := json.Marshal(u)
			t.Errorf("Validate(%s) succeeded", b)
		}
	}
	sched := "AAAAAAAAAAAA012345012345"
	if err := (&Update{Schedule: &sched}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParseEnum(t *testing.T) {
	if v, err := ParseEnum(Speeds, "speed", "TURBO"); err != nil || v != 5 {
		t.Fatalf("got %d, %v", v, err)
	}
	if _, err := ParseEnum(Modes, "mode", "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTCP(t *testing.T) {
	key, _ := ParseToken(testToken)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var hdr [2]byte
			if _, err := io.ReadFull(conn, hdr[:]); err != nil {
				return
			}
			msg := make([]byte, binary.LittleEndian.Uint16(hdr[:]))
			if _, err := io.ReadFull(conn, msg); err != nil {
				return
			}
			plain, err := decrypt(key, msg)
			if err != nil {
				return
			}
			var req map[string]any
			json.Unmarshal(plain, &req)
			resp := map[string]any{"id": req["id"], "data": map[string]any{"ts": 5, "model": 3}}
			out, _ := json.Marshal(resp)
			enc, _ := encrypt(key, out)
			conn.Write(append(binary.LittleEndian.AppendUint16(nil, uint16(len(enc))), enc...))
		}
	}()

	c, err := NewClient(ln.Addr().String(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	c.Network = "tcp"
	c.Timeout = time.Second
	defer c.Close()

	s, err := c.GetState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ModelName() != "A3" {
		t.Fatalf("model = %q", s.ModelName())
	}
}
