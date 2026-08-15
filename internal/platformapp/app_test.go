package platformapp

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/datasources/market"
	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/channel/filefeed"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

func TestPortConflictFailsFast(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, err = New(testOptions(t, listener.Addr().String()))
	if err == nil {
		t.Fatal("expected port conflict")
	}
}

func TestCancelAndRepeatedClose(t *testing.T) {
	app, err := New(testOptions(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	waitHTTP(t, "http://"+app.Addr()+"/api/health")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateDisablesLiveChannelsAndMutations(t *testing.T) {
	options := testOptions(t, "127.0.0.1:0")
	var traderStarted atomic.Bool
	options.StartTrader = func(context.Context, TraderOptions, market.Service, llm.Provider, *channel.Manager) {
		traderStarted.Store(true)
	}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.llm != nil {
		t.Fatal("shadow constructed LLM provider")
	}
	if app.alert != nil {
		t.Fatal("shadow constructed alert engine")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	waitHTTP(t, "http://"+app.Addr()+"/api/health")
	request, _ := http.NewRequest(http.MethodPost, "http://"+app.Addr()+"/api/config/reload", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if _, exists := app.channels.Get("would-be-live"); exists {
		t.Fatal("live channel was constructed")
	}
	if traderStarted.Load() {
		t.Fatal("shadow started trader")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOwnerSnapshotsRemainAvailableAfterFileFeedDisable(t *testing.T) {
	app, err := New(testOptions(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	var boltOutput bytes.Buffer
	boltResult, err := app.SnapshotBolt(context.Background(), &boltOutput)
	if err != nil {
		t.Fatal(err)
	}
	if boltResult.Kind != store.BoltSnapshotKind || boltResult.Size != int64(boltOutput.Len()) || boltOutput.Len() == 0 {
		t.Fatalf("bolt result=%+v bytes=%d", boltResult, boltOutput.Len())
	}

	feed, ok := app.channels.Get("news-feed")
	if !ok {
		t.Fatal("shadow filefeed was not constructed")
	}
	message := model.NewNewsMessage("snapshot title", "body")
	message.Source = "snapshot-test"
	message.CreateTime = time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	if err := feed.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := app.channels.DisableChannel("news-feed"); err != nil {
		t.Fatal(err)
	}
	defer app.channels.Remove("news-feed")

	var feedOutput bytes.Buffer
	feedResult, err := app.SnapshotFeed(context.Background(), &feedOutput)
	if err != nil {
		t.Fatal(err)
	}
	if feedResult.Kind != filefeed.SnapshotKindV1 || feedResult.Size != int64(feedOutput.Len()) {
		t.Fatalf("feed result=%+v bytes=%d", feedResult, feedOutput.Len())
	}
	if len(feedOutput.Bytes()) == 0 || feedOutput.Bytes()[feedOutput.Len()-1] != '\n' {
		t.Fatalf("feed snapshot is incomplete: %q", feedOutput.Bytes())
	}
}

func testOptions(t *testing.T, addr string) Options {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := candidate.InitRoot(dir); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf(`store:
  path: %q
channels:
  - name: would-be-live
    type: unsupported-live
    enabled: true
  - name: news-feed
    type: filefeed
    mode: push
    enabled: true
    options:
      path: %q
sources: []
llm:
  api_key: dummy-must-not-be-used
alert:
  enabled: true
`, filepath.Join(dir, "configured.db"), filepath.Join(dir, "configured.jsonl"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		ConfigPath: configPath, ListenAddr: addr, StorePath: filepath.Join(dir, "candidate.db"),
		CandidateRoot: dir, Shadow: true, ShadowFeedPath: filepath.Join(dir, "shadow.jsonl"),
	}
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("HTTP server did not start")
}
