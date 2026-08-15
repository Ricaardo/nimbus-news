package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/core"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
)

type toggleLifecycleSource struct {
	name string
}

func (s *toggleLifecycleSource) Name() string                   { return s.name }
func (*toggleLifecycleSource) Type() string                     { return "api-toggle-lifecycle" }
func (*toggleLifecycleSource) Fetch() ([]*model.Message, error) { return nil, nil }

func TestDisabledCreateAndToggleReconstructRuntime(t *testing.T) {
	created := 0
	source.Register("api-toggle-lifecycle", func(cfg source.Config) (source.Source, error) {
		created++
		return &toggleLifecycleSource{name: cfg.Name}, nil
	})
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := config.NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewNewsEngine(core.NewsEngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, 0)
	server.SetNewsEngine(engine)

	assertConfigStatus(t, server, "POST", "/api/sources",
		`{"name":"disabled","type":"api-toggle-lifecycle","enabled":false}`, 201)
	stored, ok := manager.GetSourceConfig("disabled")
	if !ok || stored.Enabled == nil || *stored.Enabled || created != 0 {
		t.Fatalf("disabled create stored=%+v created=%d", stored, created)
	}

	engine.Start(context.Background())
	defer engine.Stop()
	assertConfigStatus(t, server, "POST", "/api/sources/disabled/toggle", `{"enabled":true}`, 200)
	if created != 1 || !engine.IsSourceRunning("disabled") {
		t.Fatalf("initially disabled source was not reconstructed: created=%d running=%v", created, engine.IsSourceRunning("disabled"))
	}
	assertConfigStatus(t, server, "PUT", "/api/sources/disabled",
		`{"type":"api-toggle-lifecycle","enabled":false}`, 200)
	if engine.IsSourceRunning("disabled") {
		t.Fatal("PUT disabled source remained running")
	}
	assertConfigStatus(t, server, "POST", "/api/sources/disabled/toggle", `{"enabled":true}`, 200)
	if created != 2 || !engine.IsSourceRunning("disabled") {
		t.Fatalf("PUT-disabled source was not reconstructed: created=%d running=%v", created, engine.IsSourceRunning("disabled"))
	}
}

func TestSourcesHealthIncludesAwaitingSchedule(t *testing.T) {
	source.Register("api-health-scheduled", func(cfg source.Config) (source.Source, error) {
		return &toggleLifecycleSource{name: cfg.Name}, nil
	})
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := config.NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewNewsEngine(core.NewsEngineConfig{
		Sources:      []core.SourceConfig{{Name: "scheduled", Type: "api-health-scheduled", Schedule: []string{"23:59"}}},
		HealthConfig: source.HealthConfig{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, 0)
	server.SetNewsEngine(engine)

	request := httptest.NewRequest(http.MethodGet, "/api/sources/health?details=true", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Summary struct {
			Total            int `json:"total"`
			AwaitingSchedule int `json:"awaiting_schedule"`
		} `json:"summary"`
		Sources []source.SourceHealth `json:"sources"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.Total != 1 || response.Summary.AwaitingSchedule != 1 ||
		len(response.Sources) != 1 || response.Sources[0].Status != source.StatusAwaitingSchedule {
		t.Fatalf("response=%+v", response)
	}
}
