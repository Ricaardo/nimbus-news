package digest

import (
	"strings"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func TestClusterAndRenderAreDeterministic(t *testing.T) {
	at := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	items := []Item{
		{ID: "b", Source: "two", CreatedAt: at.Add(time.Minute), Message: &model.Message{Title: "Markets rally again"}},
		{ID: "c", Source: "three", CreatedAt: at, Message: &model.Message{Title: "Rates fall"}},
		{ID: "a", Source: "one", CreatedAt: at, Message: &model.Message{Title: "Markets rally"}},
	}
	first := Render(items, 10)
	second := Render([]Item{items[2], items[0], items[1]}, 10)
	if first != second {
		t.Fatalf("render depends on input order:\n%s\n---\n%s", first, second)
	}
	want := "- Markets rally（one）\n- Markets rally again（two）\n- Rates fall（three）"
	if first != want {
		t.Fatalf("render=%q want=%q", first, want)
	}
}

func TestTopicKeyIsBriefingIsolatedAndNormalizesStableURL(t *testing.T) {
	at := time.Date(2026, 8, 1, 5, 59, 0, 0, time.UTC)
	first := TopicKey(USPreview, &model.Message{Title: "First", Link: "https://EXAMPLE.com/a/../story?utm=x#fragment"}, at)
	second := TopicKey(USPreview, &model.Message{Title: "Changed", Link: "http://example.com/story?utm=y"}, at.Add(24*time.Hour))
	if first != second || !strings.HasPrefix(first, "topic:v1:") {
		t.Fatalf("stable URL keys differ: %q %q", first, second)
	}
	if TopicKey(Closing, &model.Message{Link: "https://example.com/story"}, at) == first {
		t.Fatal("briefings must be isolated")
	}
	withoutURL := &model.Message{Title: "  Rates: FALL! "}
	if TopicKey(USPreview, withoutURL, at) != TopicKey(USPreview, &model.Message{Title: "rates fall"}, at) {
		t.Fatal("normalized titles in the same six-hour bucket must match")
	}
	if TopicKey(USPreview, withoutURL, at) == TopicKey(USPreview, withoutURL, at.Add(6*time.Hour)) {
		t.Fatal("URL-less topics must be isolated by six-hour bucket")
	}
}

func TestLeaseTopicKeyAliasesExactTitlesWithoutChangingDurableIdentity(t *testing.T) {
	at := time.Date(2026, 8, 1, 5, 59, 0, 0, time.UTC)
	firstMessage := &model.Message{Title: " Apple: RALLIES! ", Link: "https://first.example/story"}
	secondMessage := &model.Message{Title: "apple rallies", Link: "https://second.example/other"}
	firstDurable := TopicKey(USPreview, firstMessage, at)
	secondDurable := TopicKey(USPreview, secondMessage, at)
	if firstDurable == secondDurable {
		t.Fatal("publisher-specific durable URL identities unexpectedly match")
	}
	if LeaseTopicKey(USPreview, firstMessage, firstDurable) != LeaseTopicKey(USPreview, secondMessage, secondDurable) {
		t.Fatal("exact normalized titles must share an ephemeral lease identity")
	}
	if LeaseTopicKey(Closing, firstMessage, firstDurable) == LeaseTopicKey(USPreview, firstMessage, firstDurable) {
		t.Fatal("lease identities must remain briefing-isolated")
	}
	if got := LeaseTopicKey(USPreview, &model.Message{Content: "fallback"}, firstDurable); got != firstDurable {
		t.Fatalf("title-less lease identity=%q want durable %q", got, firstDurable)
	}
}

func TestLeaseTopicKeyNormalizesBasicAccentedTitles(t *testing.T) {
	at := time.Date(2026, 8, 1, 5, 59, 0, 0, time.UTC)
	accented := &model.Message{Title: "Café raises résumé guidance"}
	plain := &model.Message{Title: "cafe raises resume guidance"}
	if LeaseTopicKey(USPreview, accented, TopicKey(USPreview, accented, at)) !=
		LeaseTopicKey(USPreview, plain, TopicKey(USPreview, plain, at)) {
		t.Fatal("basic accent variants should share a lease topic")
	}
}

func TestRenderTopicsUsesOneBoundedLineAndAllSources(t *testing.T) {
	topics := []Topic{{Key: "topic", Priority: 10, Items: []Item{
		{ID: "b", Source: "z", Message: &model.Message{Title: "newer"}},
		{ID: "a", Source: "a", Message: &model.Message{Title: strings.Repeat("长", 110)}},
	}}}
	rendered := RenderTopics(topics, 12)
	if strings.Count(rendered, "\n") != 0 || !strings.Contains(rendered, "（a、z）") || !strings.Contains(rendered, "…") {
		t.Fatalf("rendered=%q", rendered)
	}
}

func TestRenderTopicsRepresentativeIsUsefulRecentAndPermutationStable(t *testing.T) {
	at := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	items := []Item{
		{ID: "old-empty", Source: "z", CreatedAt: at, Message: &model.Message{Title: "empty"}},
		{ID: "new-short", Source: "b", CreatedAt: at.Add(2 * time.Minute), Message: &model.Message{Title: "short", Content: "brief"}},
		{ID: "old-long", Source: "a", CreatedAt: at.Add(time.Minute), Message: &model.Message{Title: "informative", Content: "a much longer body", ShortContent: "summary", ImageURL: "image"}},
	}
	topic := Topic{Key: "topic", Priority: 10, Items: items}
	first := RenderTopics([]Topic{topic}, 12)
	topic.Items = []Item{items[1], items[2], items[0]}
	second := RenderTopics([]Topic{topic}, 12)
	want := "- informative（a、b、z）"
	if first != want || second != want {
		t.Fatalf("representative is unstable or uninformative: first=%q second=%q want=%q", first, second, want)
	}

	tied := Topic{Key: "tied", Items: []Item{
		{ID: "b", CreatedAt: at, Message: &model.Message{Title: "older", Content: "same"}},
		{ID: "c", CreatedAt: at.Add(time.Minute), Message: &model.Message{Title: "newer", Content: "same"}},
	}}
	if got := RenderTopics([]Topic{tied}, 12); got != "- newer（）" {
		t.Fatalf("recency tie-break rendered %q", got)
	}
	tied.Items = []Item{
		{ID: "b", CreatedAt: at, Message: &model.Message{Title: "larger id", Content: "same"}},
		{ID: "a", CreatedAt: at, Message: &model.Message{Title: "smaller id", Content: "same"}},
	}
	if got := RenderTopics([]Topic{tied}, 12); got != "- smaller id（）" {
		t.Fatalf("ID tie-break rendered %q", got)
	}
}
