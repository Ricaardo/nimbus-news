package discord

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func TestWebhookSendHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	raw, err := NewDiscordChannel(channel.Config{
		Name: "discord", Mode: channel.ModePush, Webhook: server.URL,
		Options: map[string]interface{}{"webhook_plain": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = raw.Send(ctx, &model.Message{ID: "stable", Content: "test"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("send error=%v", err)
	}
}

func TestRetryWaitHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := waitForContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled retry wait took %s", elapsed)
	}
}
