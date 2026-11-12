package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
)

const (
	torrentSourceMaxBytes       = 16 << 20
	torrentSourceRequestTimeout = 30 * time.Second
	torrentSourceMaxRedirects   = 5
	torrentSourceUserAgent      = "Emby-Auto/0.1"
)

var (
	// 用于脱敏的稳定错误，避免将原始 URL/host/query 放入 Failure.Cause。
	errTorrentSourceInvalidURL    = errors.New("torrent source url is invalid")
	errTorrentSourceRequestFailed = errors.New("torrent source request failed")
	errTorrentSourceResolveFailed = errors.New("torrent source host resolution failed")
)

// torrentSourceResolver 抽象 DNS 解析，便于测试注入；生产路径使用系统解析器。
type torrentSourceResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// torrentSourceOptions 为测试提供局部注入能力；生产入口 defaultTorrentSourceFetcher
// 注入系统解析器且不允许私网，测试可替换解析器与 HTTP 客户端。
type torrentSourceOptions struct {
	allowPrivate bool
	newClient    func(domain.NetworkProxySettings) (*http.Client, error)
	// resolver 为零时仅做字面量校验；defaultTorrentSourceFetcher 在生产线注入系统解析器，
	// 测试可注入静态解析器避免依赖运行环境 DNS。
	resolver     torrentSourceResolver
	proxyEnabled bool
}

// torrentSourceFetcher 获取 HTTP(S) 种子文件，需经过应用代理且不泄露敏感信息。
type torrentSourceFetcher func(context.Context, string, domain.NetworkProxySettings) ([]byte, error)

func defaultTorrentSourceFetcher(ctx context.Context, rawURL string, proxySettings domain.NetworkProxySettings) ([]byte, error) {
	return fetchTorrentSource(ctx, rawURL, proxySettings, torrentSourceOptions{resolver: net.DefaultResolver})
}

func fetchTorrentSource(ctx context.Context, rawURL string, proxySettings domain.NetworkProxySettings, opts torrentSourceOptions) ([]byte, error) {
	// 代理启用时由代理解析目标域名，本地解析失败不构成永久无效。
	opts.proxyEnabled = proxySettings.Enabled
	if err := validateTorrentSourceURL(ctx, rawURL, opts); err != nil {
		return nil, err
	}
	newClient := proxyhttp.NewClient
	if opts.newClient != nil {
		newClient = opts.newClient
	}
	httpClient, err := newClient(proxySettings)
	if err != nil {
		return nil, permanentFailure("torrent_source_invalid", "网络代理配置无效", err)
	}
	httpClient.Timeout = torrentSourceRequestTimeout
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= torrentSourceMaxRedirects {
			return permanentFailure("torrent_source_invalid", "下载链接重定向过多", nil)
		}
		if err := validateTorrentSourceURL(req.Context(), req.URL.String(), opts); err != nil {
			return err
		}
		return nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return nil, permanentFailure("torrent_source_invalid", "下载链接无效", errTorrentSourceInvalidURL)
	}
	request.Header.Set("User-Agent", torrentSourceUserAgent)
	request.Header.Set("Accept", "application/x-bittorrent, application/octet-stream, */*")

	response, err := httpClient.Do(request)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return nil, failure
		}
		if errors.Is(err, context.Canceled) {
			return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", context.DeadlineExceeded)
		}
		return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", errTorrentSourceRequestFailed)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusTooManyRequests || (response.StatusCode >= 500 && response.StatusCode < 600) {
		return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", fmt.Errorf("HTTP %d", response.StatusCode))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, permanentFailure("torrent_source_unavailable", "种子文件下载失败", fmt.Errorf("HTTP %d", response.StatusCode))
	}
	if response.ContentLength > torrentSourceMaxBytes {
		return nil, permanentFailure("torrent_source_too_large", "种子文件过大", nil)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, torrentSourceMaxBytes+1))
	if err != nil {
		// 读取错误可能包裹底层网络错误，避免直接透传包含 URL 的原始错误。
		if errors.Is(err, context.Canceled) {
			return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", context.DeadlineExceeded)
		}
		return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", errTorrentSourceRequestFailed)
	}
	if int64(len(body)) > torrentSourceMaxBytes {
		return nil, permanentFailure("torrent_source_too_large", "种子文件过大", nil)
	}
	if len(body) == 0 {
		return nil, permanentFailure("torrent_source_not_torrent", "种子文件无效", nil)
	}
	if !isTorrentBytes(body) {
		return nil, permanentFailure("torrent_source_not_torrent", "种子文件无效", nil)
	}
	return body, nil
}

func validateTorrentSourceURL(ctx context.Context, rawURL string, opts torrentSourceOptions) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return permanentFailure("torrent_source_invalid", "下载链接无效", errTorrentSourceInvalidURL)
	}
	if parsed.User != nil {
		return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
	}
	host := parsed.Hostname()
	if host == "" {
		return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
	}
	if isLocalhostTorrentSourceHost(host) {
		return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
	}
	// 字面量 IP 阻断：loopback/private/link-local/unspecified/multicast
	if blocked := isBlockedTorrentSourceHost(host, opts.allowPrivate); blocked {
		return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
	}
	if opts.allowPrivate || isTorrentSourceIPLiteral(host) {
		return nil
	}
	// 域名解析后再判定，防止“字面量合法、解析后指向内网”的链接绕过。
	// 解析失败时沿用原有请求路径（由实际连接返回可重试失败），避免本地 DNS
	// 与代理解析主体不一致时把可用链接误判为永久无效。
	if opts.resolver == nil {
		return nil
	}
	addresses, lookupErr := opts.resolver.LookupNetIP(ctx, "ip", host)
	if lookupErr != nil || len(addresses) == 0 {
		if opts.proxyEnabled {
			return nil
		}
		return retryableFailure("torrent_source_unavailable", "暂时无法确认种子链接目标", errTorrentSourceResolveFailed)
	}
	for _, address := range addresses {
		if isBlockedNetipAddr(address) {
			return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
		}
	}
	return nil
}

func isTorrentSourceIPLiteral(host string) bool {
	if _, err := netip.ParseAddr(strings.TrimRight(host, ".")); err == nil {
		return true
	}
	return net.ParseIP(host) != nil
}

// isLocalhostTorrentSourceHost 按 URL host 语义归一化后拒绝 localhost 及其变体。
// 归一化：去除尾点、大小写不敏感；拒绝精确 localhost 以及任意 .localhost 保留域后缀（如 foo.localhost）。
// 注意：本函数不做 DNS 解析，仅基于字面量 host 判断，避免扩大网络架构；DNS 重绑定防护不在本轮范围。
func isLocalhostTorrentSourceHost(host string) bool {
	trimmed := strings.TrimRight(host, ".")
	lower := strings.ToLower(trimmed)
	if lower == "localhost" {
		return true
	}
	if strings.HasSuffix(lower, ".localhost") {
		return true
	}
	return false
}

func isBlockedTorrentSourceHost(host string, allowPrivate bool) bool {
	if allowPrivate {
		return false
	}
	// 使用结构化 IP 解析处理 IPv4/IPv6 及 zone，避免简单 % 截断掩盖无效输入。
	// Hostname 已按 URL 语义归一化，zone 由 netip 校验，非法输入交由 URL/request 规则处理。
	candidates := []string{host, strings.TrimRight(host, ".")}
	for _, candidate := range candidates {
		if addr, err := netip.ParseAddr(candidate); err == nil {
			if isBlockedNetipAddr(addr) {
				return true
			}
			continue
		}
		if ip := net.ParseIP(candidate); ip != nil {
			if isBlockedTorrentSourceIP(ip) {
				return true
			}
		}
	}
	return false
}

func isBlockedNetipAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() {
		return true
	}
	if addr.IsPrivate() {
		return true
	}
	return false
}

func isBlockedTorrentSourceIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	return false
}

func isTorrentBytes(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != 'd' {
		return false
	}
	// 常见 torrent 必须包含 announce 或 info 键
	if !bytes.Contains(trimmed, []byte("announce")) && !bytes.Contains(trimmed, []byte("info")) {
		return false
	}
	return true
}

func isMagnetSource(uri string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(uri)), "magnet:")
}

func isHTTPSource(uri string) bool {
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}
