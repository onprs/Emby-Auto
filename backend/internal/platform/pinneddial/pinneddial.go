// Package pinneddial 将已校验的目标地址固定到拨号阶段。
//
// 仅在校验阶段解析域名而不影响实际拨号时，DNS 重绑定仍可在校验与连接之间
// 把目标切换到内网地址。Pins 保存校验通过的地址，WrapTransport 让 Transport
// 在拨号时复用这些地址；TLS SNI 与 Host 头仍由请求 URL 决定，不受影响。
package pinneddial

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// DialFunc 与 net.Dialer.DialContext 签名一致。
type DialFunc func(context.Context, string, string) (net.Conn, error)

type contextKey struct{}

// Pins 记录主机名到已校验地址的映射，可由并发请求安全读取。
type Pins struct {
	mutex   sync.RWMutex
	targets map[string][]netip.Addr
}

// New 创建空的地址固定表。
func New() *Pins {
	return &Pins{targets: make(map[string][]netip.Addr)}
}

// WithContext 将地址固定表放入请求上下文，供拨号阶段读取。
func WithContext(ctx context.Context, pins *Pins) context.Context {
	if pins == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, pins)
}

// FromContext 读取请求上下文中的地址固定表。
func FromContext(ctx context.Context) *Pins {
	pins, _ := ctx.Value(contextKey{}).(*Pins)
	return pins
}

// Add 记录主机名的已校验地址，覆盖同主机的旧记录。
func (pins *Pins) Add(host string, addresses []netip.Addr) {
	if pins == nil || len(addresses) == 0 {
		return
	}
	key := normalizeHost(host)
	if key == "" {
		return
	}
	copied := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if !address.IsValid() {
			continue
		}
		copied = append(copied, address.Unmap())
	}
	if len(copied) == 0 {
		return
	}
	pins.mutex.Lock()
	pins.targets[key] = copied
	pins.mutex.Unlock()
}

// Lookup 返回主机名的已校验地址副本。
func (pins *Pins) Lookup(host string) ([]netip.Addr, bool) {
	if pins == nil {
		return nil, false
	}
	pins.mutex.RLock()
	addresses, ok := pins.targets[normalizeHost(host)]
	pins.mutex.RUnlock()
	if !ok {
		return nil, false
	}
	return append([]netip.Addr(nil), addresses...), true
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// WrapTransport 克隆 HTTP Transport 并安装地址固定拨号器。
// 零 Transport 使用 http.DefaultTransport 的副本，保持原有环境代理语义；
// 非 *http.Transport（例如自定义 RoundTripper）保持原样，调用方需自行保证边界。
func WrapTransport(transport http.RoundTripper) http.RoundTripper {
	if transport == nil {
		transport = http.DefaultTransport
	}
	base, ok := transport.(*http.Transport)
	if !ok {
		return transport
	}
	cloned := base.Clone()
	cloned.DialContext = WrapDialContext(cloned.DialContext)
	return cloned
}

// WrapDialContext 在拨号阶段优先使用已校验地址；没有记录时沿用原拨号器。
func WrapDialContext(previous DialFunc) DialFunc {
	fallback := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return dial(ctx, network, address, previous, fallback)
		}
		pins := FromContext(ctx)
		addresses, ok := pins.Lookup(host)
		if !ok {
			return dial(ctx, network, address, previous, fallback)
		}
		var lastErr error
		for _, address := range addresses {
			conn, dialErr := dial(ctx, network, net.JoinHostPort(address.String(), port), previous, fallback)
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no pinned address for %s", host)
		}
		return nil, lastErr
	}
}

func dial(ctx context.Context, network, address string, previous DialFunc, fallback *net.Dialer) (net.Conn, error) {
	if previous != nil {
		return previous(ctx, network, address)
	}
	return fallback.DialContext(ctx, network, address)
}
