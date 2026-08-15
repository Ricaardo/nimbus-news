package signal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type goldenCase struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	AsOf     string          `json:"as_of"`
	Symbols  []string        `json:"symbols"`
	Source   string          `json:"source"`
	Facts    json.RawMessage `json:"facts"`
	SignalID string          `json:"signal_id"`
}

func TestStableIDPythonGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/python_stable_ids.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			got, err := StableID(test.Type, test.AsOf, test.Symbols, test.Source, test.Facts)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.SignalID {
				t.Fatalf("StableID=%q want %q", got, test.SignalID)
			}
			projection := Projection{AsOf: test.AsOf, Symbols: test.Symbols, Source: test.Source, Facts: test.Facts}
			var event Event
			switch test.Type {
			case TypeMacro:
				event, err = (Projector{}).Macro(projection)
			case TypeShortInterest:
				event, err = (Projector{}).Short(projection)
			case Type13F:
				event, err = (Projector{}).ThirteenF(projection)
			case TypeAnalystRating:
				event, err = (Projector{}).AnalystRating(projection)
			}
			if err != nil || event.SignalID != test.SignalID {
				t.Fatalf("project SignalID=%q want %q err=%v", event.SignalID, test.SignalID, err)
			}
			if test.Name == "unicode_nested_macro" {
				rendered, err := Render(event, RenderNewsPush)
				if err != nil {
					t.Fatal(err)
				}
				want := "[宏观] 实际利率 as_of 2026-07-23: {'date': '2026-07-22', 'value': 1, 'note': '通胀→降温'}"
				if got := rendered.(map[string]any)["text"]; got != want {
					t.Fatalf("macro render=%q want %q", got, want)
				}
			}
		})
	}
}

func TestProjectorsStoreQueryAndRender(t *testing.T) {
	projection := Projection{
		AsOf: "2026-07-22", Symbols: []string{"US:NVDA"}, Source: "finra", SourceID: "row-1",
		Facts:      json.RawMessage(`{"observations":[{"sharesShort":100,"shortRatio":1.5,"shortPercentOfFloat":2.5}]}`),
		Provenance: Provenance{RawHash: "sha256:abcdef0123456789abcdef", RetrievedAt: "2026-07-23T00:00:00Z"},
	}
	event, err := (Projector{}).Short(projection)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewJSONLStore(filepath.Join(t.TempDir(), "signals.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Query(context.Background(), Query{Type: TypeShortInterest, Symbol: "US:NVDA", Latest: true})
	if err != nil || len(rows) != 1 || rows[0].SignalID != event.SignalID {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	value, err := Render(event, RenderNewsPush)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value.(map[string]any)["text"].(string), "short ratio=1.5") {
		t.Fatalf("render=%+v", value)
	}
	markdown, err := Render(event, RenderNimbusSkill)
	if err != nil || !strings.Contains(markdown.(string), "observations: 1 row(s)") {
		t.Fatalf("render=%q err=%v", markdown, err)
	}
}

func TestShadowHandler(t *testing.T) {
	store, _ := NewJSONLStore(filepath.Join(t.TempDir(), "signals.jsonl"))
	handler := NewHandler(store)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK || response.Body.String() != "{\"fetch\":false,\"mode\":\"shadow\",\"ok\":true}\n" {
		t.Fatalf("health=%d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/render/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("empty render status=%d", response.Code)
	}
}
