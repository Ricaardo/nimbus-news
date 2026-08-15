package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRSSMessageIDFallback 验证无 <guid> 时 ID 回退到 Link，避免所有条目落到同一哈希
// (根因：ch2rss.fflow.net、rss-public.bwe-ws.com 等源不提供 <guid>，
// 直接对空字符串哈希会导致所有条目落到同一 ID，被去重误杀)
func TestRSSMessageIDFallback(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC1123Z)
	feed := `<?xml version="1.0"?>
<rss><channel>
<item><title>Story A</title><link>https://example.com/a</link><pubDate>` + now + `</pubDate></item>
<item><title>Story B</title><link>https://example.com/b</link><pubDate>` + now + `</pubDate></item>
<item><title>Story A dup</title><link>https://example.com/a</link><pubDate>` + now + `</pubDate></item>
</channel></rss>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(feed))
	}))
	defer srv.Close()

	src := &RSSSource{name: "test-rss", url: srv.URL, maxAge: 24 * time.Hour}
	msgs, err := src.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	idA, idB, idADup := msgs[0].ID, msgs[1].ID, msgs[2].ID
	if idA == idB {
		t.Errorf("items with different links but empty GUID should get different IDs, got same: %s", idA)
	}
	if idA != idADup {
		t.Errorf("items with same link should get same ID, got %s vs %s", idA, idADup)
	}
}

// TestRSS304NotModified 验证条件请求:第二次 Fetch 带 If-None-Match,
// 上游 304 时返回空且不解析 body(省流量)。
func TestRSS304NotModified(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC1123Z)
	feed := `<?xml version="1.0"?>
<rss><channel>
<item><title>Story A</title><link>https://example.com/a</link><pubDate>` + now + `</pubDate></item>
</channel></rss>`

	var requests int
	var gotIfNoneMatch bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") != "" {
			gotIfNoneMatch = true
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc123"`)
		w.Write([]byte(feed))
	}))
	defer srv.Close()

	src := &RSSSource{name: "test-rss", url: srv.URL, maxAge: 24 * time.Hour}

	// 第一次:全量抓取,保存 ETag
	msgs, err := src.Fetch()
	if err != nil {
		t.Fatalf("first Fetch() error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("first fetch expected 1 message, got %d", len(msgs))
	}
	if gotIfNoneMatch {
		t.Fatal("first request should not carry If-None-Match")
	}

	// 第二次:带 If-None-Match,上游 304 → 0 条
	msgs, err = src.Fetch()
	if err != nil {
		t.Fatalf("second Fetch() error = %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("304 fetch expected 0 messages, got %d", len(msgs))
	}
	if !gotIfNoneMatch {
		t.Fatal("second request should carry If-None-Match")
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if src.notModifiedSkips != 1 {
		t.Fatalf("notModifiedSkips = %d, want 1", src.notModifiedSkips)
	}
}

// TestRSSLastModifiedFallback 上游只给 Last-Modified 不给 ETag 时同样生效。
func TestRSSLastModifiedFallback(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC1123Z)
	feed := `<?xml version="1.0"?>
<rss><channel>
<item><title>Story A</title><link>https://example.com/a</link><pubDate>` + now + `</pubDate></item>
</channel></rss>`

	var gotIfModifiedSince bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Modified-Since") != "" {
			gotIfModifiedSince = true
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", "Mon, 02 Aug 2026 00:00:00 GMT")
		w.Write([]byte(feed))
	}))
	defer srv.Close()

	src := &RSSSource{name: "test-rss", url: srv.URL, maxAge: 24 * time.Hour}
	if _, err := src.Fetch(); err != nil {
		t.Fatalf("first Fetch() error = %v", err)
	}
	msgs, err := src.Fetch()
	if err != nil {
		t.Fatalf("second Fetch() error = %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("304 fetch expected 0 messages, got %d", len(msgs))
	}
	if !gotIfModifiedSince {
		t.Fatal("second request should carry If-Modified-Since")
	}
}
