package discord

import (
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func TestWebhookEmbedPreservesBotImage(t *testing.T) {
	channel := &DiscordChannel{}
	message := &model.Message{
		Title:      "Headline",
		Content:    "Body",
		ImageURL:   "https://cdn.example/image.jpg",
		CreateTime: time.Unix(1, 0),
	}

	botEmbed := channel.buildDiscordEmbed(message)
	webhookEmbed := embedToMap(botEmbed)
	image, ok := webhookEmbed["image"].(map[string]interface{})
	if !ok || image["url"] != message.ImageURL {
		t.Fatalf("webhook image = %#v", webhookEmbed["image"])
	}
}

func TestWebhookEmbedOmitsInvalidImage(t *testing.T) {
	channel := &DiscordChannel{}
	for _, imageURL := range []string{"", "not a URL", "file:///tmp/image.jpg"} {
		message := &model.Message{Title: "Headline", ImageURL: imageURL, CreateTime: time.Unix(1, 0)}
		webhookEmbed := embedToMap(channel.buildDiscordEmbed(message))
		if _, exists := webhookEmbed["image"]; exists {
			t.Fatalf("invalid image %q was serialized: %#v", imageURL, webhookEmbed["image"])
		}
	}
}
