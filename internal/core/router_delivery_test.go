package core

import (
	"context"
	"errors"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

type failingRouterChannel struct {
	channel.BaseChannel
	err error
}

func (failing *failingRouterChannel) Send(context.Context, *model.Message) error { return failing.err }
func (failing *failingRouterChannel) SendBatch(context.Context, []*model.Message) error {
	return failing.err
}
func (failing *failingRouterChannel) Reply(context.Context, string, *model.Message) error {
	return failing.err
}
func (*failingRouterChannel) Start(context.Context) error { return nil }
func (*failingRouterChannel) Stop() error                 { return nil }

func TestDispatchNewsPropagatesChannelFailure(t *testing.T) {
	deliveryErr := errors.New("filefeed unavailable")
	manager := channel.NewManager()
	manager.Add(&failingRouterChannel{
		BaseChannel: channel.NewBaseChannel("filefeed", "filefeed", channel.ModePush),
		err:         deliveryErr,
	})
	router := NewRouter(manager)
	if err := router.DispatchNews(context.Background(), &model.Message{}, []string{"filefeed"}); !errors.Is(err, deliveryErr) {
		t.Fatalf("dispatch error=%v", err)
	}
}
