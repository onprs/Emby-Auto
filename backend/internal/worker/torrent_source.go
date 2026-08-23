package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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

// 允许测试中使用 127.0.0.1 等本地地址，避免 httptest 无法使用的问题
var allowPrivateTorrentSourceForTest = false

// torrentSourceFetcher 获取 HTTP(S) 种子文件，需经过应用代理且不泄露敏感信息。
type torrentSourceFetcher func(context.Context, string, domain.NetworkProxySettings) ([]byte, error)

func defaultTorrentSourceFetcher(ctx context.Context, rawURL string, proxySettings domain.NetworkProxySettings) ([]byte, error) {
	if err := validateTorrentSourceURL(rawURL); err != nil {
		return nil, err
	}
	httpClient, err := proxyhttp.NewClient(proxySettings)
	if err != nil {
		return nil, permanentFailure("torrent_source_invalid", "网络代理配置无效", err)
	}
	httpClient.Timeout = torrentSourceRequestTimeout
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= torrentSourceMaxRedirects {
			return permanentFailure("torrent_source_invalid", "下载链接重定向过多", nil)
		}
		if err := validateTorrentSourceURL(req.URL.String()); err != nil {
			return err
		}
		return nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return nil, permanentFailure("torrent_source_invalid", "下载链接无效", err)
	}
	request.Header.Set("User-Agent", torrentSourceUserAgent)
	request.Header.Set("Accept", "application/x-bittorrent, application/octet-stream, */*")

	response, err := httpClient.Do(request)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return nil, failure
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", err)
		}
		return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", err)
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
		return nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", err)
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

func validateTorrentSourceURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return permanentFailure("torrent_source_invalid", "下载链接无效", err)
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
	if strings.EqualFold(host, "localhost") {
		return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedTorrentSourceIP(ip) && !allowPrivateTorrentSourceForTest {
			return permanentFailure("torrent_source_invalid", "下载链接无效", nil)
		}
	}
	return nil
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
