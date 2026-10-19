package agentapi

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
)

func agentConnectivityHandler(t *testing.T, protocol int, calls *atomic.Int32) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if protocol != 0 && request.ProtoMajor != protocol {
			t.Errorf("request protocol = %s, want HTTP/%d", request.Proto, protocol)
		}
		if request.URL.Path != "/v1/chat/completions" || request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request = %s %s, authorization present = %t", request.Method, request.URL.Path, request.Header.Get("Authorization") != "")
		}
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		last := payload.Messages[len(payload.Messages)-1]
		writer.Header().Set("Content-Type", "application/json")
		call := agentTestToolCall("echo-1", "agent_connectivity_echo", `{"value":"emby-auto-agent-connectivity-v1"}`)
		if last.Role == "tool" {
			if last.ToolCallID != "echo-1" || last.Content != `{"echoed":"emby-auto-agent-connectivity-v1"}` {
				t.Errorf("echo result = %#v", last)
			}
			call = agentTestToolCall("submit-1", "submit_agent_connectivity", `{"ok":true,"echoed":"emby-auto-agent-connectivity-v1"}`)
		}
		_, _ = io.WriteString(writer, agentToolResponse([]toolCall{call}, "tool_calls"))
	})
}

func TestConnectivityTransportProtocols(t *testing.T) {
	for _, test := range []struct {
		name string
		tls  bool
		h2   bool
	}{
		{name: "http_1_1"},
		{name: "https_1_1", tls: true},
		{name: "https_2", tls: true, h2: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			protocol := 1
			if test.h2 {
				protocol = 2
			}
			server := httptest.NewUnstartedServer(agentConnectivityHandler(t, protocol, &calls))
			server.EnableHTTP2 = test.h2
			if test.tls {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			client, err := NewClient(ClientOptions{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer client.http.CloseIdleConnections()
			if err := client.ConnectivityTest(context.Background()); err != nil || calls.Load() != 2 {
				t.Fatalf("ConnectivityTest() = %v, requests = %d", err, calls.Load())
			}
		})
	}
}

func TestConnectivityThroughHTTPAndHTTPSProxies(t *testing.T) {
	for _, secureProxy := range []bool{false, true} {
		for _, secureTarget := range []bool{false, true} {
			t.Run(fmt.Sprintf("proxy_tls_%t/target_tls_%t", secureProxy, secureTarget), func(t *testing.T) {
				var targetCalls, proxyCalls atomic.Int32
				target := httptest.NewUnstartedServer(agentConnectivityHandler(t, 1, &targetCalls))
				if secureTarget {
					target.StartTLS()
				} else {
					target.Start()
				}
				defer target.Close()
				targetURL, err := url.Parse(target.URL)
				if err != nil {
					t.Fatal(err)
				}
				forward := httputil.NewSingleHostReverseProxy(targetURL)
				proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					proxyCalls.Add(1)
					if request.Method != http.MethodConnect {
						forward.ServeHTTP(writer, request)
						return
					}
					if request.Host != targetURL.Host {
						t.Errorf("CONNECT host = %s, want %s", request.Host, targetURL.Host)
						writer.WriteHeader(http.StatusBadGateway)
						return
					}
					upstream, err := net.DialTimeout("tcp", targetURL.Host, time.Second)
					if err != nil {
						t.Error(err)
						writer.WriteHeader(http.StatusBadGateway)
						return
					}
					defer func() { _ = upstream.Close() }()
					downstream, buffered, err := writer.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = downstream.Close() }()
					_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
					if err := buffered.Flush(); err != nil {
						t.Error(err)
						return
					}
					copied := make(chan struct{})
					go func() {
						_, _ = io.Copy(upstream, buffered)
						_ = upstream.Close()
						close(copied)
					}()
					_, _ = io.Copy(downstream, upstream)
					_ = downstream.Close()
					<-copied
				}))
				if secureProxy {
					proxy.StartTLS()
				} else {
					proxy.Start()
				}
				defer proxy.Close()
				httpClient, err := proxyhttp.NewClient(domain.NetworkProxySettings{Enabled: true, URL: proxy.URL})
				if err != nil {
					t.Fatal(err)
				}
				defer httpClient.CloseIdleConnections()
				roots := x509.NewCertPool()
				if secureTarget {
					roots.AddCert(target.Certificate())
				}
				if secureProxy {
					roots.AddCert(proxy.Certificate())
				}
				transport := httpClient.Transport.(*http.Transport)
				transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
				client, err := NewClient(ClientOptions{BaseURL: target.URL + "/v1", APIKey: "test-key", Model: "test-model", HTTPClient: httpClient, RequestTimeout: 2 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				if err := client.ConnectivityTest(context.Background()); err != nil || targetCalls.Load() != 2 || proxyCalls.Load() == 0 {
					t.Fatalf("ConnectivityTest() = %v, target/proxy requests = %d/%d", err, targetCalls.Load(), proxyCalls.Load())
				}
			})
		}
	}
}

func TestCompleteLimitsDecompressedGzipResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(writer)
		_, _ = io.WriteString(compressed, strings.Repeat(" ", maxResponseBytes+1)+`{}`)
		_ = compressed.Close()
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	_, err = client.complete(context.Background(), chatRequest{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "agent_response_too_large" {
		t.Fatalf("complete() = %#v, want decompressed response size limit", err)
	}
}

func TestToolLoopRejectsOtherAPIResponseFormats(t *testing.T) {
	for name, body := range map[string]string{
		"responses": `{"output":[{"type":"function_call","call_id":"1","name":"submit_result","arguments":"{}"}]}`,
		"messages":  `{"type":"message","content":[{"type":"tool_use","id":"1","name":"submit_result","input":{}}]}`,
		"sse":       "data: {\"choices\":[]}\n\ndata: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			client := agentTestClient(t, body)
			_, err := client.RunToolLoop(context.Background(), agentTestLoopRequest())
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != "agent_response_invalid" {
				t.Fatalf("RunToolLoop() = %#v, want invalid response", err)
			}
		})
	}
}

func TestClientConcurrentToolLoopsKeepConversationsIndependent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		calls.Add(1)
		call := agentTestToolCall("read-1", "read_context", `{}`)
		if payload.Messages[len(payload.Messages)-1].Role == "tool" {
			call = agentTestToolCall("submit-1", "submit_result", `{}`)
		}
		_, _ = io.WriteString(writer, agentToolResponse([]toolCall{call}, "tool_calls"))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	const concurrency = 32
	failures := make(chan error, concurrency)
	var group sync.WaitGroup
	for range concurrency {
		group.Go(func() {
			result, err := client.RunToolLoop(context.Background(), agentTestLoopRequest())
			if err != nil || len(result.Steps) != 2 || string(result.Submission) != `{}` {
				failures <- fmt.Errorf("result = %#v, error = %w", result, err)
			}
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if calls.Load() != concurrency*2 {
		t.Fatalf("request count = %d, want %d", calls.Load(), concurrency*2)
	}
}
