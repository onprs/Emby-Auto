package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// WebHandler 在同一入口提供网页，并将 API 请求交给原有处理链。
type WebHandler struct {
	api  http.Handler
	root *os.Root
}

func NewWebHandler(api http.Handler, directory string) (*WebHandler, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("WEB_ROOT 必须指向可读取的网页目录")
	}
	index, err := root.Open("index.html")
	if err != nil {
		_ = root.Close()
		return nil, errors.New("WEB_ROOT 缺少可读取的 index.html")
	}
	info, err := index.Stat()
	_ = index.Close()
	if err != nil || !info.Mode().IsRegular() {
		_ = root.Close()
		return nil, errors.New("WEB_ROOT 的 index.html 必须是普通文件")
	}
	return &WebHandler{api: api, root: root}, nil
}

func (handler *WebHandler) Close() error {
	return handler.root.Close()
}

func (handler *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		handler.api.ServeHTTP(w, r)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "请求方法不可用", http.StatusMethodNotAllowed)
		return
	}

	name := strings.Trim(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		http.NotFound(w, r)
		return
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") {
			http.NotFound(w, r)
			return
		}
	}

	file, err := handler.root.Open(name)
	// 只有网页路由回退到首页；缺失的资源保持 404。
	if os.IsNotExist(err) && path.Ext(name) == "" && !strings.HasPrefix(name, "assets/") {
		name = "index.html"
		file, err = handler.root.Open(name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "no-cache")
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}
