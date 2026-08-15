package feishu

import (
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func TestBuildCardDoesNotUseExternalURLAsImageKey(t *testing.T) {
	channel := &FeishuChannel{}
	card := channel.buildCard(&model.Message{
		Title:    "Headline",
		ImageURL: "https://cdn.example/image.jpg",
	})

	elements := card["elements"].([]map[string]interface{})
	var foundFallback bool
	for _, element := range elements {
		if element["tag"] == "img" {
			t.Fatalf("external URL emitted as image element: %#v", element)
		}
		text, _ := element["text"].(map[string]interface{})
		if text["content"] == "图片: https://cdn.example/image.jpg" {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatalf("external image fallback missing: %#v", elements)
	}
}

func TestBuildCardUsesExplicitValidFeishuImageKey(t *testing.T) {
	channel := &FeishuChannel{}
	message := &model.Message{
		Title:    "Headline",
		ImageURL: "https://cdn.example/image.jpg",
		Metadata: map[string]interface{}{"feishu_image_key": "img_v2_abc-123"},
	}
	card := channel.buildCard(message)

	elements := card["elements"].([]map[string]interface{})
	for _, element := range elements {
		if element["tag"] == "img" {
			if element["img_key"] != "img_v2_abc-123" {
				t.Fatalf("img_key=%v", element["img_key"])
			}
			return
		}
	}
	t.Fatalf("valid image key not emitted: %#v", elements)
}

func TestBuildCardOmitsUnsafeImageValue(t *testing.T) {
	channel := &FeishuChannel{}
	message := &model.Message{
		Title:    "Headline",
		ImageURL: "javascript:alert(1)",
		Metadata: map[string]interface{}{"feishu_image_key": "img_bad/key"},
	}
	card := channel.buildCard(message)

	for _, element := range card["elements"].([]map[string]interface{}) {
		if element["tag"] == "img" {
			t.Fatalf("unsafe image emitted as img: %#v", element)
		}
		if text, ok := element["text"].(map[string]interface{}); ok &&
			text["content"] == "图片: javascript:alert(1)" {
			t.Fatalf("unsafe image emitted as fallback: %#v", element)
		}
	}
}
