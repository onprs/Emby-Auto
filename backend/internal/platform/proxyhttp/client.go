package proxyhttp

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

// NewClient creates an HTTP client whose proxy behavior is isolated from process environment variables.
func NewClient(settings domain.NetworkProxySettings) (*http.Client, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport has an unexpected type")
	}
	transport := base.Clone()
	transport.Proxy = nil

	rawURL := strings.TrimSpace(settings.URL)
	if !settings.Enabled {
		return &http.Client{Transport: transport}, nil
	}
	if rawURL == "" {
		return nil, fmt.Errorf("network proxy URL is required when enabled")
	}
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, fmt.Errorf("network proxy URL must be HTTP(S) without embedded credentials")
	}
	transport.Proxy = http.ProxyURL(parsed)
	return &http.Client{Transport: transport}, nil
}
