package channel

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

type managerTestChannel struct {
	BaseChannel
	err       error
	active    *atomic.Int32
	maxActive *atomic.Int32
}

func newManagerTestChannel(name string, err error, active, maxActive *atomic.Int32) *managerTestChannel {
	return &managerTestChannel{BaseChannel: NewBaseChannel(name, "test", ModePush), err: err, active: active, maxActive: maxActive}
}

func (channel *managerTestChannel) Send(context.Context, *model.Message) error {
	active := channel.active.Add(1)
	for {
		maximum := channel.maxActive.Load()
		if active <= maximum || channel.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	channel.active.Add(-1)
	return channel.err
}
func (channel *managerTestChannel) SendBatch(context.Context, []*model.Message) error {
	return channel.err
}
func (channel *managerTestChannel) Reply(context.Context, string, *model.Message) error {
	return channel.err
}
func (channel *managerTestChannel) Start(context.Context) error { return nil }
func (channel *managerTestChannel) Stop() error                 { return nil }

func TestBroadcastSendsConcurrentlyAndAggregatesErrors(t *testing.T) {
	firstErr := errors.New("first failed")
	secondErr := errors.New("second failed")
	var active, maxActive atomic.Int32
	manager := NewManager()
	manager.Add(newManagerTestChannel("first", firstErr, &active, &maxActive))
	manager.Add(newManagerTestChannel("second", secondErr, &active, &maxActive))
	manager.Add(newManagerTestChannel("success", nil, &active, &maxActive))

	err := manager.Broadcast(context.Background(), &model.Message{}, []string{"first", "second", "success"})
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("aggregated error=%v", err)
	}
	if maxActive.Load() < 2 {
		t.Fatalf("sends were not concurrent, max active=%d", maxActive.Load())
	}
}

func TestSendToPropagatesFailureAndMissingChannel(t *testing.T) {
	sendErr := errors.New("delivery failed")
	var active, maximum atomic.Int32
	manager := NewManager()
	manager.Add(newManagerTestChannel("failing", sendErr, &active, &maximum))
	if err := manager.SendTo(context.Background(), &model.Message{}, "failing"); !errors.Is(err, sendErr) {
		t.Fatalf("SendTo error=%v", err)
	}
	if err := manager.SendTo(context.Background(), &model.Message{}, "missing"); err == nil {
		t.Fatal("missing channel accepted")
	}
}
