package main

import (
	"crypto/sha1"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fixtureHash    = "0123456789abcdef0123456789abcdef01234567"
	fixtureRSSHash = "1123456789abcdef0123456789abcdef01234567"
)

var fixtureASS = []byte(`[Script Info]
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,24,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.00,0:00:02.00,Default,,0,0,0,,Fixture subtitle
`)

type torrentState struct {
	Hash     string
	SavePath string
	Category string
	Resumed  bool
}

type fixtureServer struct {
	mu               sync.Mutex
	torrent          *torrentState
	categories       map[string]struct{}
	failures         map[string]bool
	animeLibraryRoot string
	movieLibraryRoot string
	controlDir       string
}

func main() {
	address := flag.String("address", "127.0.0.1:19090", "HTTP listen address")
	animeLibraryRoot := flag.String("anime-library-root", "", "anime library root inspected by the Emby fixture")
	movieLibraryRoot := flag.String("movie-library-root", "", "movie library root inspected by the Emby fixture")
	controlDir := flag.String("control-dir", "", "directory for cross-process E2E controls")
	flag.Parse()

	fixture := &fixtureServer{
		categories: make(map[string]struct{}), failures: make(map[string]bool), animeLibraryRoot: *animeLibraryRoot,
		movieLibraryRoot: *movieLibraryRoot, controlDir: *controlDir,
	}
	server := &http.Server{Addr: *address, Handler: fixture.routes()}
	log.Printf("E2E fixture listening on %s", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func (fixture *fixtureServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /control/failure", fixture.controlFailure)
	mux.HandleFunc("GET /control/status", fixture.controlStatus)
	mux.HandleFunc("GET /search", fixture.search)
	mux.HandleFunc("GET /detail/fixture", fixture.searchDetail)
	mux.HandleFunc("GET /rss.xml", fixture.rss)
	mux.HandleFunc("/api/v2/", fixture.qbittorrent)
	mux.HandleFunc("/tmdb/", fixture.tmdb)
	mux.HandleFunc("/emby/", fixture.emby)
	return mux
}

func (fixture *fixtureServer) controlFailure(writer http.ResponseWriter, request *http.Request) {
	target := strings.TrimSpace(request.URL.Query().Get("target"))
	enabled, err := strconv.ParseBool(request.URL.Query().Get("enabled"))
	if target == "" || err != nil {
		http.Error(writer, "target and boolean enabled are required", http.StatusBadRequest)
		return
	}
	fixture.mu.Lock()
	fixture.failures[target] = enabled
	fixture.mu.Unlock()
	if fixture.controlDir != "" && (strings.HasPrefix(target, "media_") || target == "api_restart") {
		marker := filepath.Join(fixture.controlDir, target)
		if enabled {
			if err := os.WriteFile(marker, []byte("requested\n"), 0o644); err != nil {
				http.Error(writer, err.Error(), http.StatusInternalServerError)
				return
			}
			if target == "api_restart" {
				_ = os.Remove(filepath.Join(fixture.controlDir, "api_restarted"))
			}
		} else {
			_ = os.Remove(marker)
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (fixture *fixtureServer) controlStatus(writer http.ResponseWriter, request *http.Request) {
	target := strings.TrimSpace(request.URL.Query().Get("target"))
	enabled := fixture.failing(target)
	if fixture.controlDir != "" && target == "api_restarted" {
		_, err := os.Stat(filepath.Join(fixture.controlDir, target))
		enabled = err == nil
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]bool{"enabled": enabled})
}

func (fixture *fixtureServer) failing(target string) bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.failures[target]
}

func (fixture *fixtureServer) search(writer http.ResponseWriter, request *http.Request) {
	if fixture.failing("search_slow") {
		time.Sleep(750 * time.Millisecond)
	}
	if fixture.failing("search") {
		http.Error(writer, "fixture search failure", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	run := request.URL.Query().Get("query")
	_, _ = fmt.Fprintf(writer, `<!doctype html><a href="/detail/fixture?run=%s">[Fixture] Fixture Show - S01E01</a>`, url.QueryEscape(run))
}

func (fixture *fixtureServer) searchDetail(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	hash := fixtureHash
	if run := request.URL.Query().Get("run"); run != "" {
		hash = fixtureHashFor("search:" + run)
	}
	_, _ = fmt.Fprintf(writer, `<!doctype html><a href="magnet:?xt=urn:btih:%s">magnet</a>`, hash)
}

func (fixture *fixtureServer) rss(writer http.ResponseWriter, request *http.Request) {
	if fixture.failing("rss") {
		http.Error(writer, "fixture RSS failure", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/rss+xml")
	hash := fixtureRSSHash
	if run := request.URL.Query().Get("run"); run != "" {
		hash = fixtureHashFor("rss:" + run)
	}
	_, _ = fmt.Fprintf(writer, `<?xml version="1.0"?><rss version="2.0"><channel><title>Fixture Show</title><item><guid>fixture-s01e01-%s</guid><title>[Fixture] Fixture Show - S01E01</title><link>http://fixture.invalid/episode/1</link><enclosure type="application/x-bittorrent" url="magnet:?xt=urn:btih:%s" length="4096"/></item></channel></rss>`, hash[:12], hash)
}

func fixtureHashFor(seed string) string {
	value := sha1.Sum([]byte(seed))
	return fmt.Sprintf("%x", value)
}

func (fixture *fixtureServer) qbittorrent(writer http.ResponseWriter, request *http.Request) {
	if fixture.failing("qbittorrent") {
		http.Error(writer, "fixture qBittorrent failure", http.StatusServiceUnavailable)
		return
	}
	switch request.URL.Path {
	case "/api/v2/auth/login":
		_, _ = fmt.Fprint(writer, "Ok.")
	case "/api/v2/torrents/info":
		fixture.writeTorrents(writer)
	case "/api/v2/torrents/add":
		fixture.addTorrent(writer, request)
	case "/api/v2/torrents/files":
		fixture.writeTorrentFiles(writer)
	case "/api/v2/torrents/filePrio":
		writer.WriteHeader(http.StatusOK)
	case "/api/v2/torrents/categories":
		fixture.mu.Lock()
		categories := make(map[string]map[string]string, len(fixture.categories))
		for category := range fixture.categories {
			categories[category] = map[string]string{"name": category, "savePath": ""}
		}
		fixture.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(categories)
	case "/api/v2/torrents/createCategory":
		if err := request.ParseForm(); err != nil || strings.TrimSpace(request.FormValue("category")) == "" {
			http.Error(writer, "category is required", http.StatusBadRequest)
			return
		}
		fixture.mu.Lock()
		fixture.categories[strings.TrimSpace(request.FormValue("category"))] = struct{}{}
		fixture.mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	case "/api/v2/torrents/setCategory":
		if err := request.ParseForm(); err != nil || strings.TrimSpace(request.FormValue("category")) == "" {
			http.Error(writer, "category is required", http.StatusBadRequest)
			return
		}
		fixture.mu.Lock()
		if fixture.torrent != nil {
			fixture.torrent.Category = strings.TrimSpace(request.FormValue("category"))
		}
		fixture.mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	case "/api/v2/torrents/removeCategories":
		if err := request.ParseForm(); err != nil {
			http.Error(writer, "invalid categories", http.StatusBadRequest)
			return
		}
		fixture.mu.Lock()
		for _, category := range strings.Split(request.FormValue("categories"), "\n") {
			delete(fixture.categories, strings.TrimSpace(category))
		}
		fixture.mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	case "/api/v2/torrents/start", "/api/v2/torrents/resume":
		fixture.mu.Lock()
		if fixture.torrent != nil {
			fixture.torrent.Resumed = true
		}
		fixture.mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	case "/api/v2/torrents/delete":
		fixture.mu.Lock()
		fixture.torrent = nil
		fixture.mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *fixtureServer) addTorrent(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		if formErr := request.ParseForm(); formErr != nil {
			http.Error(writer, "invalid form", http.StatusBadRequest)
			return
		}
	}
	savePath := request.FormValue("savepath")
	category := request.FormValue("category")
	hash, err := magnetHash(request.FormValue("urls"))
	if savePath == "" || category == "" || err != nil {
		http.Error(writer, "savepath, category, and a valid magnet URL are required", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(savePath, 0o755); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	video := filepath.Join(savePath, "Fixture Show - S01E01.mkv")
	subtitle := filepath.Join(savePath, "Fixture Show - S01E01.zh-Hans.ass")
	if err := os.WriteFile(video, []byte("fixture-video-payload\n"), 0o644); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(subtitle, fixtureASS, 0o644); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	fixture.mu.Lock()
	fixture.torrent = &torrentState{Hash: hash, SavePath: savePath, Category: category}
	fixture.mu.Unlock()
	writer.WriteHeader(http.StatusOK)
}

func (fixture *fixtureServer) writeTorrents(writer http.ResponseWriter) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	if fixture.torrent == nil {
		_, _ = fmt.Fprint(writer, "[]")
		return
	}
	progress := 0.25
	state := "pausedDL"
	amountLeft := int64(100)
	if fixture.torrent.Resumed {
		progress = 1
		state = "uploading"
		amountLeft = 0
	}
	_ = json.NewEncoder(writer).Encode([]map[string]any{{
		"hash": fixture.torrent.Hash, "name": "Fixture Show", "state": state, "progress": progress,
		"amount_left": amountLeft, "content_path": filepath.Join(fixture.torrent.SavePath, "Fixture Show - S01E01.mkv"),
		"save_path": fixture.torrent.SavePath, "size": 4096, "total_size": 4096, "category": fixture.torrent.Category,
	}})
}

func magnetHash(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return "", fmt.Errorf("invalid magnet URL")
	}
	value := strings.TrimPrefix(strings.ToLower(parsed.Query().Get("xt")), "urn:btih:")
	if len(value) != 40 {
		return "", fmt.Errorf("invalid BTIH")
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", fmt.Errorf("invalid BTIH")
		}
	}
	return value, nil
}

func (fixture *fixtureServer) writeTorrentFiles(writer http.ResponseWriter) {
	fixture.mu.Lock()
	state := fixture.torrent
	fixture.mu.Unlock()
	if state == nil {
		http.Error(writer, "torrent not found", http.StatusNotFound)
		return
	}
	videoInfo, _ := os.Stat(filepath.Join(state.SavePath, "Fixture Show - S01E01.mkv"))
	subtitleInfo, _ := os.Stat(filepath.Join(state.SavePath, "Fixture Show - S01E01.zh-Hans.ass"))
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode([]map[string]any{
		{"index": 0, "name": "Fixture Show - S01E01.mkv", "size": videoInfo.Size(), "progress": 1, "priority": 1, "is_seed": false},
		{"index": 1, "name": "Fixture Show - S01E01.zh-Hans.ass", "size": subtitleInfo.Size(), "progress": 1, "priority": 1, "is_seed": false},
	})
}

func (fixture *fixtureServer) tmdb(writer http.ResponseWriter, request *http.Request) {
	if fixture.failing("tmdb") {
		http.Error(writer, "fixture TMDb failure", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/tmdb/configuration":
		_, _ = fmt.Fprint(writer, `{}`)
	case "/tmdb/search/tv":
		_, _ = fmt.Fprint(writer, `{"results":[{"id":100,"name":"Fixture Show","original_name":"Fixture Show","first_air_date":"2026-01-01","overview":"E2E fixture series"}]}`)
	case "/tmdb/search/movie":
		_, _ = fmt.Fprint(writer, `{"results":[{"id":200,"title":"Fixture Movie","original_title":"Fixture Movie","release_date":"2024-03-08","overview":"E2E fixture movie"}]}`)
	case "/tmdb/tv/100":
		_, _ = fmt.Fprint(writer, `{"id":100,"name":"Fixture Show","original_name":"Fixture Show","seasons":[{"id":1001,"name":"Season 1","season_number":1,"episode_count":1}]}`)
	case "/tmdb/tv/100/season/1":
		_, _ = fmt.Fprint(writer, `{"id":1001,"name":"Season 1","season_number":1,"episodes":[{"id":10001,"name":"Pilot","episode_number":1,"air_date":"2026-01-01"}]}`)
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *fixtureServer) emby(writer http.ResponseWriter, request *http.Request) {
	if fixture.failing("emby") {
		http.Error(writer, "fixture Emby failure", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/emby/Library/Refresh":
		writer.WriteHeader(http.StatusNoContent)
	case "/emby/Library/VirtualFolders":
		_ = json.NewEncoder(writer).Encode([]map[string]any{
			{"Name": "Fixture Library", "ItemId": "library-fixture", "CollectionType": "tvshows", "Locations": []string{fixture.animeLibraryRoot}},
			{"Name": "Fixture Movies", "ItemId": "library-movies", "CollectionType": "movies", "Locations": []string{fixture.movieLibraryRoot}},
		})
	case "/emby/Items":
		fixture.writeEmbyItems(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *fixtureServer) writeEmbyItems(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Query().Get("ParentId") == "library-movies" {
		items := []map[string]any{}
		if videoPath := firstVideoPath(fixture.movieLibraryRoot); videoPath != "" {
			items = append(items, map[string]any{
				"Id": "movie-fixture", "ParentId": "library-movies", "Type": "Movie", "Name": "Fixture Movie",
				"Path": videoPath, "ProviderIds": map[string]string{"Tmdb": "200"},
			})
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
		return
	}
	items := []map[string]any{
		{"Id": "series-fixture", "ParentId": "library-fixture", "Type": "Series", "Name": "Fixture Show", "ProviderIds": map[string]string{"Tmdb": "100"}},
		{"Id": "season-fixture", "ParentId": "series-fixture", "Type": "Season", "Name": "Season 1", "IndexNumber": 1, "ProviderIds": map[string]string{}},
	}
	if videoPath := firstVideoPath(fixture.animeLibraryRoot); videoPath != "" {
		items = append(items, map[string]any{"Id": "episode-fixture", "ParentId": "season-fixture", "Type": "Episode", "Name": "Pilot", "Path": videoPath, "IndexNumber": 1, "ParentIndexNumber": 1, "ProviderIds": map[string]string{"Tmdb": "10001"}})
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
}

func firstVideoPath(root string) string {
	videoPath := ""
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && isVideoExtension(filepath.Ext(path)) && videoPath == "" {
			videoPath = path
		}
		return nil
	})
	return videoPath
}

func isVideoExtension(extension string) bool {
	return strings.EqualFold(extension, ".mkv") || strings.EqualFold(extension, ".mp4") || strings.EqualFold(extension, ".webm")
}
