//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestRSSSubtitleGroupHTTPIntegration(t *testing.T) {
	ctx := context.Background()
	_, pool := testutil.NewMigratedPostgres(t)
	actor := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, 'subtitle-http-test', 'fixture-hash')`, actor); err != nil {
		t.Fatal(err)
	}
	workflow := service.NewRSSSubtitleGroupWorkflow(db.New(pool), database.NewTransactor(pool))
	auth := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: actor, Username: "subtitle-http-test"}}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(auth, false), WithRSSSubtitleGroups(workflow)))
	request := func(method, path, name string) *httptest.ResponseRecorder {
		var body string
		if method == http.MethodPost || method == http.MethodPut {
			data, err := json.Marshal(map[string]string{"name": name})
			if err != nil {
				t.Fatal(err)
			}
			body = string(data)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	const path = "/api/v1/rss/subtitle-groups"
	response := request(http.MethodPost, path, "【ＲｅｌｅａｓｅＴｅａｍ】")
	if response.Code != http.StatusCreated {
		t.Fatalf("新增 = %d %s", response.Code, response.Body.String())
	}
	var group RSSSubtitleGroup
	if err := json.NewDecoder(response.Body).Decode(&group); err != nil || group.Name != "ReleaseTeam" {
		t.Fatalf("新增响应 = %#v, %v", group, err)
	}
	groupPath := path + "/" + group.Id.String()
	t.Run("重复名称返回409", func(t *testing.T) {
		r := request(http.MethodPost, path, "releaseteam")
		if r.Code != http.StatusConflict {
			t.Fatalf("重复 = %d %s", r.Code, r.Body.String())
		}
	})
	for _, test := range []struct{ name, value string }{
		{"空名称", ""}, {"空白", " "}, {"超长", strings.Repeat("字", 129)},
		{"空字符", "Release\x00Team"}, {"控制字符", "Release\u0081Team"}, {"仅不可见字符", "\u200b"},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			t.Run(method+"/"+test.name, func(t *testing.T) {
				target := path
				if method == http.MethodPut {
					target = groupPath
				}
				r := request(method, target, test.value)
				if r.Code != http.StatusBadRequest {
					t.Fatalf("输入 %q 返回 %d %s，期望400", test.value, r.Code, r.Body.String())
				}
				var apiError ApiError
				if err := json.NewDecoder(r.Body).Decode(&apiError); err != nil || apiError.Code != "invalid_rss_subtitle_group" {
					t.Fatalf("校验错误响应 = %#v, %v", apiError, err)
				}
			})
		}
	}
	for _, name := range []string{"A", "123", "!!!", "AACStudio"} {
		t.Run("维护名称可用于完整标签/"+name, func(t *testing.T) {
			r := request(http.MethodPost, path, name)
			if r.Code != http.StatusCreated {
				t.Fatalf("新增 = %d %s", r.Code, r.Body.String())
			}
			names, err := workflow.ListRSSSubtitleGroupNames(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := domain.ExtractRSSSubtitleGroupCandidates(domain.RSSFeed{Entries: []domain.RSSFeedEntry{{Title: "[" + name + "] 动画 - 01"}}}, names)
			if len(got) != 1 || got[0] != name {
				t.Fatalf("已持久化名称 %q 的识别结果 = %#v", name, got)
			}
		})
	}
	t.Run("重命名", func(t *testing.T) {
		r := request(http.MethodPut, groupPath, "ReleaseTranslationTeam")
		if r.Code != http.StatusOK {
			t.Fatalf("重命名 = %d %s", r.Code, r.Body.String())
		}
	})
	t.Run("删除返回无正文204", func(t *testing.T) {
		r := request(http.MethodDelete, groupPath, "")
		if r.Code != http.StatusNoContent || r.Body.Len() != 0 {
			t.Fatalf("删除 = %d %s", r.Code, r.Body.String())
		}
		r = request(http.MethodDelete, groupPath, "")
		if r.Code != http.StatusNotFound {
			t.Fatalf("重复删除 = %d %s", r.Code, r.Body.String())
		}
	})
}
