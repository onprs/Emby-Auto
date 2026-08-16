package qbittorrent

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxErrorBodyBytes                       = 64 << 10
	maxTorrentRateLimitBytesPerSecond int64 = math.MaxInt32 - 1 // MaxInt32 在 qBittorrent 中表示不限速。
	ManagedCategory                         = "emby_auto"
	RuntimePausedTag                        = "emby_auto_runtime_paused"
)

var ErrTorrentHashNotConfirmed = errors.New("qBittorrent added torrent hash was not confirmed")

type Torrent struct {
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	AmountLeft    int64   `json:"amount_left"`
	ContentPath   string  `json:"content_path"`
	SavePath      string  `json:"save_path"`
	Size          int64   `json:"size"`
	TotalSize     int64   `json:"total_size"`
	Category      string  `json:"category"`
	Tags          string  `json:"tags"`
	DownloadLimit int64   `json:"dl_limit"`
	UploadLimit   int64   `json:"up_limit"`
}

type TorrentFile struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
	IsSeed   bool    `json:"is_seed"`
}

type ClientOptions struct {
	BaseURL        string
	Username       string
	Password       string
	RequestTimeout time.Duration
	PollInterval   time.Duration
	ConfirmTimeout time.Duration
	HTTPClient     *http.Client
}

type Client struct {
	baseURL        string
	username       string
	password       string
	pollInterval   time.Duration
	confirmTimeout time.Duration
	httpClient     *http.Client
}

type AddRequest struct {
	Source   string
	SavePath string
	Category string
}

type HashResolutionReason string

const (
	HashResolutionMagnet   HashResolutionReason = "magnet"
	HashResolutionExisting HashResolutionReason = "existing"
	HashResolutionNew      HashResolutionReason = "new"
)

type HashResolution struct {
	Hash   string
	Reason HashResolutionReason
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (err *HTTPError) Error() string {
	if err.Body == "" {
		return fmt.Sprintf("qBittorrent HTTP %d", err.StatusCode)
	}
	return fmt.Sprintf("qBittorrent HTTP %d: %s", err.StatusCode, err.Body)
}

func NewClient(options ClientOptions) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(options.BaseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, fmt.Errorf("qBittorrent base URL must be an HTTP(S) URL without credentials")
	}
	if options.RequestTimeout <= 0 {
		return nil, fmt.Errorf("qBittorrent request timeout must be positive")
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 500 * time.Millisecond
	}
	if options.ConfirmTimeout <= 0 {
		options.ConfirmTimeout = 15 * time.Second
	}

	var httpClient *http.Client
	if options.HTTPClient == nil {
		jar, jarErr := cookiejar.New(nil)
		if jarErr != nil {
			return nil, fmt.Errorf("create qBittorrent cookie jar: %w", jarErr)
		}
		httpClient = &http.Client{Jar: jar, Timeout: options.RequestTimeout}
	} else {
		clone := *options.HTTPClient
		if clone.Jar == nil {
			jar, jarErr := cookiejar.New(nil)
			if jarErr != nil {
				return nil, fmt.Errorf("create qBittorrent cookie jar: %w", jarErr)
			}
			clone.Jar = jar
		}
		clone.Timeout = options.RequestTimeout
		httpClient = &clone
	}

	return &Client{
		baseURL:        strings.TrimRight(parsed.String(), "/"),
		username:       options.Username,
		password:       options.Password,
		pollInterval:   options.PollInterval,
		confirmTimeout: options.ConfirmTimeout,
		httpClient:     httpClient,
	}, nil
}

func (client *Client) Login(ctx context.Context) error {
	response, err := client.postForm(ctx, "/api/v2/auth/login", url.Values{
		"username": {client.username},
		"password": {client.password},
	})
	if err != nil {
		return fmt.Errorf("qBittorrent login: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if err != nil {
		return fmt.Errorf("read qBittorrent login response: %w", err)
	}
	if strings.TrimSpace(string(body)) != "Ok." {
		return fmt.Errorf("qBittorrent login rejected credentials")
	}
	return nil
}

func (client *Client) ListTorrents(ctx context.Context, category string) ([]Torrent, error) {
	query := url.Values{}
	if category != "" {
		query.Set("category", category)
	}
	response, err := client.get(ctx, "/api/v2/torrents/info", query)
	if err != nil {
		return nil, fmt.Errorf("list qBittorrent torrents: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var torrents []Torrent
	if err := json.NewDecoder(response.Body).Decode(&torrents); err != nil {
		return nil, fmt.Errorf("decode qBittorrent torrents: %w", err)
	}
	return torrents, nil
}

// AddAndConfirm snapshots a correlation scope, adds the torrent until its
// metadata is available, and waits until qBittorrent exposes one unambiguous
// actual torrent hash. qBittorrent stops the torrent at the metadata boundary
// so file priorities can be set before payload download begins.
func (client *Client) AddAndConfirm(ctx context.Context, request AddRequest) (HashResolution, error) {
	request.Source = strings.TrimSpace(request.Source)
	request.Category = strings.TrimSpace(request.Category)
	if request.Source == "" {
		return HashResolution{}, fmt.Errorf("torrent source must not be blank")
	}
	if request.Category == "" {
		return HashResolution{}, fmt.Errorf("torrent category must not be blank")
	}

	categoryScope := request.Category
	if ExtractBTIH(request.Source) != "" {
		categoryScope = ""
	}
	before, err := client.ListTorrents(ctx, categoryScope)
	if err != nil {
		return HashResolution{}, err
	}
	if resolution, ok := existingCorrelatedTorrent(before, request.Source); ok {
		return resolution, nil
	}
	response, err := client.postForm(ctx, "/api/v2/torrents/add", url.Values{
		"urls":          {request.Source},
		"savepath":      {request.SavePath},
		"category":      {request.Category},
		"stopped":       {"false"},
		"stopCondition": {"MetadataReceived"},
	})
	if err != nil {
		return HashResolution{}, fmt.Errorf("add qBittorrent torrent: %w", err)
	}
	_ = response.Body.Close()

	deadline := time.NewTimer(client.confirmTimeout)
	defer deadline.Stop()
	for {
		after, listErr := client.ListTorrents(ctx, categoryScope)
		if listErr != nil {
			return HashResolution{}, listErr
		}
		if resolution, ok := ResolveAddedTorrentHash(before, after, request.Source); ok {
			return resolution, nil
		}

		timer := time.NewTimer(client.pollInterval)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return HashResolution{}, ctx.Err()
		case <-deadline.C:
			stopTimer(timer)
			expected := ExtractBTIH(request.Source)
			if expected != "" {
				return HashResolution{}, fmt.Errorf("%w: expected %s", ErrTorrentHashNotConfirmed, expected)
			}
			return HashResolution{}, ErrTorrentHashNotConfirmed
		case <-timer.C:
		}
	}
}

func (client *Client) TorrentFiles(ctx context.Context, hash string) ([]TorrentFile, error) {
	normalized := normalizeTorrentHash(hash)
	if normalized == "" {
		return nil, fmt.Errorf("invalid torrent hash")
	}
	response, err := client.get(ctx, "/api/v2/torrents/files", url.Values{"hash": {normalized}})
	if err != nil {
		return nil, fmt.Errorf("list qBittorrent torrent files: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var files []TorrentFile
	if err := json.NewDecoder(response.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("decode qBittorrent torrent files: %w", err)
	}
	return files, nil
}

func (client *Client) SetFilePriority(ctx context.Context, hash string, fileIndexes []int, priority int) error {
	normalized := normalizeTorrentHash(hash)
	if normalized == "" {
		return fmt.Errorf("invalid torrent hash")
	}
	if priority != 0 && priority != 1 && priority != 6 && priority != 7 {
		return fmt.Errorf("unsupported qBittorrent file priority %d", priority)
	}
	if len(fileIndexes) == 0 {
		return nil
	}
	indexes := append([]int(nil), fileIndexes...)
	sort.Ints(indexes)
	parts := make([]string, 0, len(indexes))
	previous := -1
	for _, index := range indexes {
		if index < 0 {
			return fmt.Errorf("qBittorrent file index must be nonnegative")
		}
		if index == previous {
			continue
		}
		parts = append(parts, strconv.Itoa(index))
		previous = index
	}
	response, err := client.postForm(ctx, "/api/v2/torrents/filePrio", url.Values{
		"hash":     {normalized},
		"id":       {strings.Join(parts, "|")},
		"priority": {strconv.Itoa(priority)},
	})
	if err != nil {
		return fmt.Errorf("set qBittorrent file priority: %w", err)
	}
	_ = response.Body.Close()
	return nil
}

func (client *Client) SetTorrentRateLimits(ctx context.Context, hash string, downloadBytesPerSecond, uploadBytesPerSecond int64) error {
	normalized := normalizeTorrentHash(hash)
	if normalized == "" {
		return fmt.Errorf("invalid torrent hash")
	}
	if downloadBytesPerSecond < 0 || downloadBytesPerSecond > maxTorrentRateLimitBytesPerSecond ||
		uploadBytesPerSecond < 0 || uploadBytesPerSecond > maxTorrentRateLimitBytesPerSecond {
		return fmt.Errorf("qBittorrent rate limits must be between 0 and %d bytes/s", maxTorrentRateLimitBytesPerSecond)
	}
	limits := []struct {
		endpoint string
		label    string
		value    int64
	}{
		{endpoint: "/api/v2/torrents/setDownloadLimit", label: "download", value: downloadBytesPerSecond},
		{endpoint: "/api/v2/torrents/setUploadLimit", label: "upload", value: uploadBytesPerSecond},
	}
	for _, limit := range limits {
		response, err := client.postForm(ctx, limit.endpoint, url.Values{
			"hashes": {normalized},
			"limit":  {strconv.FormatInt(limit.value, 10)},
		})
		if err != nil {
			return fmt.Errorf("set qBittorrent torrent %s rate limit: %w", limit.label, err)
		}
		_ = response.Body.Close()
	}
	return nil
}

func (client *Client) ResumeTorrent(ctx context.Context, hash string) error {
	return client.ResumeTorrents(ctx, []string{hash})
}

func (client *Client) ResumeTorrents(ctx context.Context, hashes []string) error {
	return client.changeTorrentState(ctx, hashes, []string{"/api/v2/torrents/start", "/api/v2/torrents/resume"}, "resume")
}

func (client *Client) StopTorrents(ctx context.Context, hashes []string) error {
	return client.changeTorrentState(ctx, hashes, []string{"/api/v2/torrents/stop", "/api/v2/torrents/pause"}, "stop")
}

func (client *Client) AddTorrentTags(ctx context.Context, hashes []string, tags ...string) error {
	return client.changeTorrentTags(ctx, "/api/v2/torrents/addTags", hashes, tags)
}

func (client *Client) RemoveTorrentTags(ctx context.Context, hashes []string, tags ...string) error {
	return client.changeTorrentTags(ctx, "/api/v2/torrents/removeTags", hashes, tags)
}

func (client *Client) changeTorrentState(ctx context.Context, hashes, endpoints []string, action string) error {
	normalized, err := normalizeTorrentHashes(hashes)
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		return nil
	}
	var lastError error
	for _, endpoint := range endpoints {
		response, requestErr := client.postForm(ctx, endpoint, url.Values{"hashes": {strings.Join(normalized, "|")}})
		if requestErr == nil {
			_ = response.Body.Close()
			return nil
		}
		lastError = requestErr
	}
	return fmt.Errorf("%s qBittorrent torrents: %w", action, lastError)
}

func (client *Client) changeTorrentTags(ctx context.Context, endpoint string, hashes, tags []string) error {
	normalizedHashes, err := normalizeTorrentHashes(hashes)
	if err != nil {
		return err
	}
	if len(normalizedHashes) == 0 {
		return nil
	}
	normalizedTags := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || strings.ContainsAny(tag, ",|") {
			return fmt.Errorf("invalid qBittorrent torrent tag")
		}
		normalizedTags = append(normalizedTags, tag)
	}
	if len(normalizedTags) == 0 {
		return fmt.Errorf("qBittorrent torrent tags must not be empty")
	}
	response, err := client.postForm(ctx, endpoint, url.Values{
		"hashes": {strings.Join(normalizedHashes, "|")},
		"tags":   {strings.Join(normalizedTags, ",")},
	})
	if err != nil {
		return fmt.Errorf("change qBittorrent torrent tags: %w", err)
	}
	_ = response.Body.Close()
	return nil
}

func TorrentHasTag(torrent Torrent, tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return false
	}
	for _, candidate := range strings.Split(torrent.Tags, ",") {
		if strings.TrimSpace(candidate) == tag {
			return true
		}
	}
	return false
}

func IsTorrentStopped(torrent Torrent) bool {
	state := strings.ToLower(strings.TrimSpace(torrent.State))
	return strings.HasPrefix(state, "paused") || strings.HasPrefix(state, "stopped")
}

func normalizeTorrentHashes(hashes []string) ([]string, error) {
	normalized := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		hash = normalizeTorrentHash(hash)
		if hash == "" {
			return nil, fmt.Errorf("invalid torrent hash")
		}
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		normalized = append(normalized, hash)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func (client *Client) EnsureCategory(ctx context.Context, category string) error {
	category = strings.TrimSpace(category)
	if category == "" {
		return fmt.Errorf("torrent category must not be blank")
	}
	exists, err := client.categoryExists(ctx, category)
	if err != nil {
		return fmt.Errorf("check qBittorrent torrent category: %w", err)
	}
	if exists {
		return nil
	}

	response, createErr := client.postForm(ctx, "/api/v2/torrents/createCategory", url.Values{
		"category": {category},
		"savePath": {""},
	})
	if createErr == nil {
		_ = response.Body.Close()
		return nil
	}

	// Another worker may have created the category after the initial read.
	exists, checkErr := client.categoryExists(ctx, category)
	if checkErr == nil && exists {
		return nil
	}
	if checkErr != nil {
		createErr = errors.Join(createErr, checkErr)
	}
	return fmt.Errorf("create qBittorrent torrent category: %w", createErr)
}

func (client *Client) categoryExists(ctx context.Context, category string) (bool, error) {
	response, err := client.get(ctx, "/api/v2/torrents/categories", nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	categories := map[string]json.RawMessage{}
	if err := json.NewDecoder(response.Body).Decode(&categories); err != nil {
		return false, fmt.Errorf("decode qBittorrent torrent categories: %w", err)
	}
	_, exists := categories[category]
	return exists, nil
}

func (client *Client) SetTorrentCategory(ctx context.Context, hash, category string) error {
	normalized := normalizeTorrentHash(hash)
	category = strings.TrimSpace(category)
	if normalized == "" {
		return fmt.Errorf("invalid torrent hash")
	}
	if category == "" {
		return fmt.Errorf("torrent category must not be blank")
	}
	response, err := client.postForm(ctx, "/api/v2/torrents/setCategory", url.Values{
		"hashes":   {normalized},
		"category": {category},
	})
	if err != nil {
		return fmt.Errorf("set qBittorrent torrent category: %w", err)
	}
	_ = response.Body.Close()
	return nil
}

func (client *Client) DeleteCategory(ctx context.Context, category string) error {
	category = strings.TrimSpace(category)
	if category == "" || category == ManagedCategory {
		return fmt.Errorf("temporary torrent category is invalid")
	}
	response, err := client.postForm(ctx, "/api/v2/torrents/removeCategories", url.Values{
		"categories": {category},
	})
	if err != nil {
		return fmt.Errorf("delete qBittorrent torrent category: %w", err)
	}
	_ = response.Body.Close()
	return nil
}

func (client *Client) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	normalized := normalizeTorrentHash(hash)
	if normalized == "" {
		return fmt.Errorf("invalid torrent hash")
	}
	response, err := client.postForm(ctx, "/api/v2/torrents/delete", url.Values{
		"hashes":      {normalized},
		"deleteFiles": {strconv.FormatBool(deleteFiles)},
	})
	if err != nil {
		return fmt.Errorf("delete qBittorrent torrent: %w", err)
	}
	_ = response.Body.Close()
	return nil
}

func IsTorrentComplete(torrent Torrent) bool {
	if torrent.Progress >= 1 {
		return true
	}
	if torrent.AmountLeft == 0 && torrent.TotalSize > 0 {
		return true
	}
	switch strings.ToLower(torrent.State) {
	case "uploading", "stalledup", "forcedup", "pausedup", "queuedup":
		return true
	default:
		return false
	}
}

func existingCorrelatedTorrent(torrents []Torrent, source string) (HashResolution, bool) {
	hashes := torrentHashes(torrents)
	if expected := ExtractBTIH(source); expected != "" {
		if _, exists := hashes[expected]; exists {
			return HashResolution{Hash: expected, Reason: HashResolutionExisting}, true
		}
		return HashResolution{}, false
	}
	if len(hashes) != 1 {
		return HashResolution{}, false
	}
	for hash := range hashes {
		return HashResolution{Hash: hash, Reason: HashResolutionExisting}, true
	}
	return HashResolution{}, false
}

func ResolveAddedTorrentHash(before, after []Torrent, source string) (HashResolution, bool) {
	beforeHashes := torrentHashes(before)
	afterHashes := torrentHashes(after)
	expected := ExtractBTIH(source)
	if _, exists := afterHashes[expected]; expected != "" && exists {
		reason := HashResolutionMagnet
		if _, existed := beforeHashes[expected]; existed {
			reason = HashResolutionExisting
		}
		return HashResolution{Hash: expected, Reason: reason}, true
	}

	newHashes := make([]string, 0, len(afterHashes))
	for hash := range afterHashes {
		if _, existed := beforeHashes[hash]; !existed {
			newHashes = append(newHashes, hash)
		}
	}
	if len(newHashes) != 1 {
		return HashResolution{}, false
	}
	return HashResolution{Hash: newHashes[0], Reason: HashResolutionNew}, true
}

func ExtractBTIH(source string) string {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return ""
	}
	for _, topic := range parsed.Query()["xt"] {
		const prefix = "urn:btih:"
		if len(topic) <= len(prefix) || !strings.EqualFold(topic[:len(prefix)], prefix) {
			continue
		}
		if hash := normalizeTorrentHash(topic[len(prefix):]); hash != "" {
			return hash
		}
	}
	return ""
}

func normalizeTorrentHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 40 {
		if _, err := hex.DecodeString(value); err == nil {
			return strings.ToLower(value)
		}
	}
	if len(value) != 32 {
		return ""
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(value))
	if err != nil || len(decoded) != 20 {
		return ""
	}
	return hex.EncodeToString(decoded)
}

func torrentHashes(torrents []Torrent) map[string]struct{} {
	result := make(map[string]struct{}, len(torrents))
	for _, torrent := range torrents {
		if hash := normalizeTorrentHash(torrent.Hash); hash != "" {
			result[hash] = struct{}{}
		}
	}
	return result
}

func (client *Client) get(ctx context.Context, endpoint string, query url.Values) (*http.Response, error) {
	requestURL := client.baseURL + endpoint
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	return client.do(request)
}

func (client *Client) postForm(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return client.do(request)
}

func (client *Client) do(request *http.Request) (*http.Response, error) {
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if readErr != nil {
		return nil, fmt.Errorf("read qBittorrent error response: %w", readErr)
	}
	return nil, &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
