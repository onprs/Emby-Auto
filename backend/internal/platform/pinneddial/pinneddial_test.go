package pinneddial

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestWrapDialContextUsesPinnedAddressAndPort(t *testing.T) {
	pins := New()
	pins.Add("CDN.Example.Test.", []netip.Addr{netip.MustParseAddr("203.0.113.9")})
	ctx := WithContext(context.Background(), pins)
	var dialed string
	previous := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, errors.New("stop after recording")
	}
	_, err := WrapDialContext(previous)(ctx, "tcp", "cdn.example.test:8443")
	if err == nil {
		t.Fatal("expected dial error from fake dialer")
	}
	if dialed != "203.0.113.9:8443" {
		t.Fatalf("dialed = %q, want pinned address with original port", dialed)
	}
}

func TestWrapDialContextFallsBackWithoutPin(t *testing.T) {
	ctx := WithContext(context.Background(), New())
	var dialed string
	previous := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, errors.New("stop after recording")
	}
	_, _ = WrapDialContext(previous)(ctx, "tcp", "cdn.example.test:443")
	if dialed != "cdn.example.test:443" {
		t.Fatalf("dialed = %q, want original address", dialed)
	}
}

func TestWrapDialContextTriesAllPinnedAddresses(t *testing.T) {
	pins := New()
	pins.Add("cdn.example.test", []netip.Addr{
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("203.0.113.9"),
	})
	ctx := WithContext(context.Background(), pins)
	var attempts []string
	previous := func(_ context.Context, _, address string) (net.Conn, error) {
		attempts = append(attempts, address)
		if len(attempts) == 1 {
			return nil, errors.New("first address unreachable")
		}
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}
	conn, err := WrapDialContext(previous)(ctx, "tcp", "cdn.example.test:443")
	if err != nil {
		t.Fatalf("dial error = %v", err)
	}
	_ = conn.Close()
	want := []string{"[2001:db8::1]:443", "203.0.113.9:443"}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for index := range want {
		if attempts[index] != want[index] {
			t.Fatalf("attempts = %v, want %v", attempts, want)
		}
	}
}

func TestPinsLookupReturnsCopy(t *testing.T) {
	pins := New()
	address := netip.MustParseAddr("203.0.113.9")
	pins.Add("cdn.example.test", []netip.Addr{address})
	addresses, ok := pins.Lookup("cdn.example.test")
	if !ok || len(addresses) != 1 {
		t.Fatalf("lookup = %v/%t", addresses, ok)
	}
	addresses[0] = netip.MustParseAddr("127.0.0.1")
	again, _ := pins.Lookup("cdn.example.test")
	if again[0] != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("pinned addresses were mutated through lookup: %v", again)
	}
}
