package main

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"testing"
)

func TestHTTPTransportUsesHTTPSProxyFromEnvironment(t *testing.T) {
	// ProxyFromEnvironment 会缓存第一次读取的环境。独立进程避免其他协议测试
	// 先发起 HTTP 请求后，使本测试设置的 HTTPS_PROXY 不生效。
	if os.Getenv("CLINE_TEST_PROXY_SUBPROCESS") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHTTPTransportUsesHTTPSProxyFromEnvironment$")
		cmd.Env = append(os.Environ(), "CLINE_TEST_PROXY_SUBPROCESS=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("proxy subprocess: %v\n%s", err, output)
		}
		return
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8080")
	t.Setenv("NO_PROXY", "")

	req, err := http.NewRequest(http.MethodPost, "https://api.workos.com/user_management/authorize/device", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	proxyURL, err := httpTransport.Proxy(req)
	if err != nil {
		t.Fatalf("resolve proxy: %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected HTTPS_PROXY to be selected")
	}

	want, err := url.Parse("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("parse expected proxy URL: %v", err)
	}
	if proxyURL.String() != want.String() {
		t.Fatalf("proxy URL = %q, want %q", proxyURL, want)
	}
}
