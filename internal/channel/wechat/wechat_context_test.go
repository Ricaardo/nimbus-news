package wechat

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

func TestSendHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	raw, err := NewWechatChannel(channel.Config{Name: "wechat", Mode: channel.ModePush, Webhook: server.URL})
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
