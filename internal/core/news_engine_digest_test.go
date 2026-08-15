package core

import (
	"context"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

type routingSource struct {
	name string
	msg  *model.Message
}

func (s *routingSource) Name() string { return s.name }
func (*routingSource) Type() string   { return "test" }
func (s *routingSource) Fetch() ([]*model.Message, error) {
	copy := *s.msg
	return []*model.Message{&copy}, nil
}

type routingChannel struct {
	channel.BaseChannel
	sent int
}

func (c *routingChannel) Send(context.Context, *model.Message) error {
	c.sent++
	return nil
}
func (*routingChannel) SendBatch(context.Context, []*model.Message) error { return nil }
func (*routingChannel) Reply(context.Context, string, *model.Message) error {
	return nil
}
func (*routingChannel) Start(context.Context) error { return nil }
func (*routingChannel) Stop() error                 { return nil }

type routingDigestStore struct {
	enqueued int
	target   digest.Briefing
	err      error
}

func (s *routingDigestStore) Enqueue(_ context.Context, _ string, target digest.Briefing, _ *model.Message) error {
	s.enqueued++
	s.target = target
	return s.err
}
func (*routingDigestStore) Lease(context.Context, digest.Briefing, int, time.Duration) (*digest.Lease, error) {
	return nil, nil
}
func (*routingDigestStore) Ack(context.Context, string) error { return nil }

func TestNewsEngineDeliveryModes(t *testing.T) {
	ch := &routingChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)}
	manager := channel.NewManager()
	manager.Add(ch)
	router := NewRouter(manager)
	ds := &routingDigestStore{}
	engine := &NewsEngine{router: router, digestStore: ds, ctx: context.Background()}
	src := &routingSource{name: "feed", msg: &model.Message{ID: "1", Title: "material news"}}

	if !engine.fetchAndDispatch(src, newsSourceInfo{channels: []string{"main"}, deliveryMode: "direct"}) {
		t.Fatal("direct fetch failed")
	}
	if ch.sent != 1 || ds.enqueued != 0 {
		t.Fatalf("direct: sent=%d enqueued=%d", ch.sent, ds.enqueued)
	}

	if !engine.fetchAndDispatch(src, newsSourceInfo{channels: []string{"main"}, deliveryMode: "digest", briefingTarget: digest.Closing}) {
		t.Fatal("digest fetch failed")
	}
	if ch.sent != 1 || ds.enqueued != 1 || ds.target != digest.Closing {
		t.Fatalf("digest: sent=%d enqueued=%d target=%s", ch.sent, ds.enqueued, ds.target)
	}

	if !engine.fetchAndDispatch(src, newsSourceInfo{channels: []string{"main"}, deliveryMode: "silent"}) {
		t.Fatal("silent fetch failed")
	}
	if ch.sent != 1 || ds.enqueued != 1 {
		t.Fatalf("silent: sent=%d enqueued=%d", ch.sent, ds.enqueued)
	}
}
