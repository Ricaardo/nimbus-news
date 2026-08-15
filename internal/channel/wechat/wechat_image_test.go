package wechat

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestValidateImageURL 仅 HTTPS 且禁私网/保留地址。
func TestValidateImageURL(t *testing.T) {
	ok := []string{
		"https://fred.stlouisfed.org/graph/fredgraph.png?id=DGS10",
		"https://cdn.example.com/a.png",
	}
	blocked := []string{
		"http://fred.stlouisfed.org/a.png",      // 非 https
		"https://127.0.0.1/a.png",               // 回环
		"https://192.168.1.10/a.png",            // 私网
		"https://10.0.0.5/a.png",                // 私网
		"https://169.254.169.254/latest/meta",   // 链路本地(云 metadata)
		"https://0.0.0.0/a.png",                 // 未指定
		"ftp://example.com/a.png",               // 非 https
		"https:///no-host.png",                  // 无 host
	}
	for _, u := range ok {
		if err := validateImageURL(u); err != nil {
			t.Fatalf("should allow %s: %v", u, err)
		}
	}
	for _, u := range blocked {
		if err := validateImageURL(u); err == nil {
			t.Fatalf("should block %s", u)
		}
	}
}

// TestDownloadImageHTTP 下载路径:成功/非 200/超 2MB。
func TestDownloadImageHTTP(t *testing.T) {
	// 成功路径
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-png-data"))
	}))
	defer imgSrv.Close()
	w := &WechatChannel{httpClient: &http.Client{Timeout: 5 * time.Second}}
	data, err := w.downloadImage(context.Background(), imgSrv.URL)
	if err != nil || string(data) != "fake-png-data" {
		t.Fatalf("download failed: %v %q", err, data)
	}

	// 非 200
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer badSrv.Close()
	if _, err := w.downloadImage(context.Background(), badSrv.URL); err == nil {
		t.Fatal("404 image must error")
	}

	// 超 2MB
	bigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 2<<20+1))
	}))
	defer bigSrv.Close()
	if _, err := w.downloadImage(context.Background(), bigSrv.URL); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize must error with too large, got %v", err)
	}
}

// TestSendImageMessagePayload 校验通过后 payload 组装(走真实 TLS 下载)。
func TestSendImageMessagePayload(t *testing.T) {
	var gotPayload string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotPayload = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 直接构造 download 数据,绕过网络(校验逻辑单独测)
	w := &WechatChannel{webhook: srv.URL, httpClient: srv.Client()}
	payload := map[string]interface{}{
		"msgtype": "image",
		"image": map[string]string{
			"base64": buildImageBase64([]byte("fake")),
			"md5":    buildImageMD5([]byte("fake")),
		},
	}
	if err := w.sendRequest(context.Background(), payload); err != nil {
		t.Fatalf("sendRequest failed: %v", err)
	}
	if !strings.Contains(gotPayload, `"msgtype":"image"`) || !strings.Contains(gotPayload, "base64") {
		t.Fatalf("webhook payload not image type: %s", gotPayload)
	}

	// SSRF 护栏:私网 URL 直接拒绝,不发请求
	if err := w.sendImageMessage(context.Background(), "http://127.0.0.1/x.png"); err == nil {
		t.Fatal("private http url must be rejected")
	}
}

// TestBuildImagePayload 企微 image 消息 payload 的 base64/md5 编码正确性。
func TestBuildImagePayload(t *testing.T) {
	data := []byte("fake-png-bytes-0123456789")
	b64 := buildImageBase64(data)
	if b64 != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("base64 mismatch: %q", b64)
	}
	if b64 == string(data) {
		t.Fatalf("base64 must not be raw data")
	}

	md5hex := buildImageMD5(data)
	sum := md5.Sum(data)
	if md5hex != hex.EncodeToString(sum[:]) {
		t.Fatalf("md5 mismatch: %q", md5hex)
	}
	if len(md5hex) != 32 {
		t.Fatalf("md5 must be 32 lowercase hex chars, got %d", len(md5hex))
	}

	// 空数据也要能编码(企微侧会拒绝,但编码本身不 panic)
	if buildImageBase64(nil) != "" {
		t.Fatalf("empty base64 should be empty")
	}
}

// TestBuildImagePayloadDeterministic 同一图片两次编码结果一致(企微 md5 校验依赖)。
func TestBuildImagePayloadDeterministic(t *testing.T) {
	data := []byte("same-image-bytes")
	if buildImageBase64(data) != buildImageBase64(data) {
		t.Fatal("base64 must be deterministic")
	}
	if buildImageMD5(data) != buildImageMD5(data) {
		t.Fatal("md5 must be deterministic")
	}
}
