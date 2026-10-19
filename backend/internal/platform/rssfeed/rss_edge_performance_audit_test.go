//go:build rss_audit

package rssfeed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRSSAuditFetchBodyLimitIntegration(t *testing.T) {
	const limit = 256
	const document = "<rss><channel><title>Audit Feed</title></channel></rss>"
	for _, testCase := range []struct {
		name      string
		size      int
		chunked   bool
		wantError bool
	}{
		{name: "低于上限", size: limit - 1},
		{name: "恰好达到上限", size: limit},
		{name: "超过上限", size: limit + 1, wantError: true},
		{name: "分块响应超过上限", size: limit + 1, chunked: true, wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := document + strings.Repeat(" ", testCase.size-len(document))
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if testCase.chunked {
					response.(http.Flusher).Flush()
				}
				_, _ = response.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(ClientOptions{HTTPClient: server.Client(), MaxBytes: limit})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Fetch(context.Background(), server.URL)
			if (err != nil) != testCase.wantError {
				t.Fatalf("Fetch 错误=%v，预期错误=%t", err, testCase.wantError)
			}
		})
	}
}

func BenchmarkRSSAuditFeedParse(b *testing.B) {
	for _, count := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("entries_%d", count), func(b *testing.B) {
			var document strings.Builder
			document.WriteString("<rss><channel><title>Audit Feed</title>")
			for index := 0; index < count; index++ {
				fmt.Fprintf(&document, "<item><guid>entry-%d</guid><title>Audit Series S01E%02d [1080p]</title><enclosure url=\"https://example.test/%d.torrent\" type=\"application/x-bittorrent\"/></item>", index, index%12+1, index)
			}
			document.WriteString("</channel></rss>")
			body := []byte(document.String())
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				feed, err := parse(body)
				if err != nil || len(feed.Entries) != count {
					b.Fatalf("解析结果条目数=%d 错误=%v", len(feed.Entries), err)
				}
			}
		})
	}
}
