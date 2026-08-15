package source

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func TestRSSParsesEncodedContentAndMedia(t *testing.T) {
	xmlBody := `<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel>
<item><title>Trump update</title><guid>1</guid><description>old description</description>
<content:encoded><![CDATA[First paragraph.<br>Second paragraph.]]></content:encoded>
<enclosure url="https://cdn.example/audio.mp3" type="audio/mpeg"/>
<media:content url="https://cdn.example/one.jpg" type="image/jpeg"/>
<media:content url="https://cdn.example/video.mp4" medium="video"/>
<media:thumbnail url="https://cdn.example/two.jpg"/>
</item></channel></rss>`

	messages := fetchRSSFixture(t, xmlBody, nil)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	message := messages[0]
	if message.Content != "First paragraph. Second paragraph." {
		t.Fatalf("content = %q", message.Content)
	}
	wantImages := []string{"https://cdn.example/one.jpg", "https://cdn.example/two.jpg"}
	if strings.Join(message.ImageURLs, ",") != strings.Join(wantImages, ",") {
		t.Fatalf("images = %#v", message.ImageURLs)
	}
	if message.ImageURL != wantImages[0] || message.VideoURL != "https://cdn.example/video.mp4" {
		t.Fatalf("primary image/video = %q / %q", message.ImageURL, message.VideoURL)
	}
}

func TestRSSVideoAndRepostFiltersAreConservative(t *testing.T) {
	xmlBody := `<rss><channel>
<item><title>Video only</title><guid>1</guid><description>Watch video</description><media:content url="https://cdn.example/1.mp4" type="video/mp4"/></item>
<item><title>Analysis with interview</title><guid>2</guid><description>This interview contains meaningful policy analysis.</description><media:content url="https://cdn.example/2.mp4" type="video/mp4"/></item>
<item><title>RT @account: repeated post</title><guid>3</guid><description>Repeated text with enough words.</description></item>
<item><title>Powell said “RT is not policy”</title><guid>4</guid><description>Quoted speech remains useful original reporting.</description></item>
</channel></rss>`
	options := map[string]interface{}{"exclude_video_only": true, "exclude_reposts": true}
	messages := fetchRSSFixture(t, xmlBody, options)
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].Title != "Analysis with interview" || messages[1].Title != "Powell said “RT is not policy”" {
		t.Fatalf("unexpected retained titles: %q, %q", messages[0].Title, messages[1].Title)
	}
}

func TestRSSBloombergGenericExclusions(t *testing.T) {
	xmlBody := `<rss><channel>
<item><title>Markets Rally</title><guid>1</guid><link>https://www.bloomberg.com/news/articles/markets-rally</link><description>Stocks advanced broadly.</description></item>
<item><title>Video</title><guid>2</guid><link>https://www.bloomberg.com/videos/foo</link><description>Markets video</description></item>
<item><title>Live</title><guid>3</guid><link>https://www.bloomberg.com/live/bar</link><description>Live coverage</description></item>
<item><title>Newsletter</title><guid>4</guid><link>https://www.bloomberg.com/newsletters/baz</link><description>Newsletter signup</description></item>
<item><title>Daily podcast briefing</title><guid>5</guid><link>https://www.bloomberg.com/news/articles/audio</link><description>Listen to our podcast today.</description></item>
</channel></rss>`
	options := map[string]interface{}{
		"exclude_link_patterns": []interface{}{"/videos/", "/live/", "/newsletters/"},
		"exclude_keywords":      []interface{}{"podcast"},
	}
	messages := fetchRSSFixture(t, xmlBody, options)
	if len(messages) != 1 || messages[0].Title != "Markets Rally" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
}

func TestRSSRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	created, err := NewRSSSource(Config{Name: "fixture", URL: server.URL, Options: map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = created.Fetch()
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("Fetch error = %v, want HTTP 503", err)
	}
}

func TestRSSRejectsOversizedResponse(t *testing.T) {
	const limit = 128
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", limit+1))
	}))
	defer server.Close()

	created, err := NewRSSSource(Config{
		Name: "fixture",
		URL:  server.URL,
		Options: map[string]interface{}{
			"max_response_bytes": limit,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = created.Fetch()
	if err == nil || !strings.Contains(err.Error(), "128 byte limit") {
		t.Fatalf("Fetch error = %v, want response byte limit error", err)
	}
}

func TestRSSFedLinkedBodyAndFallback(t *testing.T) {
	article := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/empty" {
			fmt.Fprint(w, `<html><main>No recognized article body.</main></html>`)
			return
		}
		fmt.Fprint(w, `<html><nav>Ignore navigation</nav><div id="article">
<div class="heading col-xs-12 col-sm-8 col-md-8"><h3>Heading</h3><p>Share controls</p></div>
<div class="col-xs-12 col-sm-8 col-md-8"><p>Official first paragraph.</p><p>Official second paragraph.</p></div>
</div></html>`)
	}))
	defer article.Close()
	articleURL, _ := url.Parse(article.URL)

	options := map[string]interface{}{
		"fetch_linked_body":     true,
		"linked_body_hosts":     []interface{}{articleURL.Hostname()},
		"linked_body_max_chars": 200,
	}
	xmlBody := fmt.Sprintf(`<rss><channel><item><title>Fed release</title><guid>1</guid><link>%s</link><description>RSS fallback text.</description></item></channel></rss>`, article.URL)
	messages := fetchRSSFixtureWithClient(t, xmlBody, options, article.Client())
	if len(messages) != 1 || messages[0].Content != "Official first paragraph. Official second paragraph." {
		t.Fatalf("linked content = %#v", messages)
	}

	xmlBody = fmt.Sprintf(`<rss><channel><item><title>Fed fallback</title><guid>fallback</guid><link>%s/empty</link><description>RSS fallback text.</description></item></channel></rss>`, article.URL)
	messages = fetchRSSFixtureWithClient(t, xmlBody, options, article.Client())
	if len(messages) != 1 || messages[0].Content != "RSS fallback text." {
		t.Fatalf("missing-body fallback = %#v", messages)
	}

	disallowed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<article><p>Must not be fetched.</p></article>`)
	}))
	defer disallowed.Close()
	disallowedURL := strings.Replace(disallowed.URL, "127.0.0.1", "localhost", 1)
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, disallowedURL, http.StatusFound)
	}))
	defer redirect.Close()
	redirectURL, _ := url.Parse(redirect.URL)
	options["linked_body_hosts"] = []interface{}{redirectURL.Hostname()}
	xmlBody = fmt.Sprintf(`<rss><channel><item><title>Fed redirect</title><guid>2</guid><link>%s</link><description>RSS fallback text.</description></item></channel></rss>`, redirect.URL)
	messages = fetchRSSFixtureWithClient(t, xmlBody, options, redirect.Client())
	if len(messages) != 1 || messages[0].Content != "RSS fallback text." {
		t.Fatalf("redirect fallback = %#v", messages)
	}
}

func fetchRSSFixture(t *testing.T, xmlBody string, options map[string]interface{}) []*model.Message {
	t.Helper()
	return fetchRSSFixtureWithClient(t, xmlBody, options, nil)
}

func fetchRSSFixtureWithClient(t *testing.T, xmlBody string, options map[string]interface{}, client *http.Client) []*model.Message {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, xmlBody)
	}))
	defer server.Close()
	if options == nil {
		options = make(map[string]interface{})
	}
	created, err := NewRSSSource(Config{Name: "fixture", URL: server.URL, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	source := created.(*RSSSource)
	source.httpClient = client
	messages, err := source.Fetch()
	if err != nil {
		t.Fatal(err)
	}
	return messages
}
