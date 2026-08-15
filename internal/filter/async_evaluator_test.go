package filter

import (
	"sync/atomic"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func TestAsyncEvaluator_SubmitReusesBlockingFilterScore(t *testing.T) {
	cases := []struct {
		name          string
		score         float64
		wantHighScore bool
	}{
		{name: "pre-scored above threshold triggers onHighScore", score: 8.0, wantHighScore: true},
		{name: "pre-scored below threshold does not trigger onHighScore", score: 3.0, wantHighScore: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var highScoreCalled int32
			onHighScore := func(msg *model.Message) {
				atomic.AddInt32(&highScoreCalled, 1)
			}

			e := NewAsyncEvaluator(&fakeProvider{}, AsyncEvalConfig{
				Enabled:        true,
				ThresholdScore: 6,
				TargetSources:  []string{"src-a"},
			}, onHighScore)

			msg := newTestMsg(tc.name)
			msg.Source = "src-a"
			msg.SetMetadata("ai_score", tc.score)

			e.Submit(msg)

			if len(e.queue) != 0 {
				t.Errorf("expected pre-scored message not to be enqueued, queue len=%d", len(e.queue))
			}
			gotHighScore := atomic.LoadInt32(&highScoreCalled) == 1
			if gotHighScore != tc.wantHighScore {
				t.Errorf("onHighScore called = %v, want %v", gotHighScore, tc.wantHighScore)
			}
		})
	}
}
