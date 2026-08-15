package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/core"
)

func TestManagementEndpointsRejectUntrustedBrowserOrigins(t *testing.T) {
	server := newConfigTestServer(t)
	for _, path := range []string{
		"/api/config",
		"/api/llm",
		"/api/channels",
		"/api/sources",
		"/api/keys",
		"/api/digest/stats",
		"/api/status",
		"/api/schedule",
	} {
		for _, method := range []string{http.MethodGet, http.MethodOptions} {
			req := httptest.NewRequest(method, path, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Origin", "https://evil.example")
			rec := httptest.NewRecorder()
			server.GetRouter().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s status=%d want=%d", method, path, rec.Code, http.StatusForbidden)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Fatalf("%s %s exposed CORS origin %q", method, path, got)
			}
		}
	}
}

func TestManagementEndpointsAllowTrustedLocalOriginAndOriginlessCLI(t *testing.T) {
	server := newConfigTestServer(t)
	for _, origin := range []string{"", "http://localhost:5173", "https://127.0.0.1:8080", "http://[::1]:3000"} {
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		server.GetRouter().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("origin=%q status=%d body=%s", origin, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("origin=%q allow-origin=%q", origin, got)
		}
	}
}

func TestManagementResponsesRecursivelyRedactSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "platform.yaml")
	t.Setenv("API_SECURITY_AUTH", "Bearer expanded-secret")
	t.Setenv("API_SECURITY_SOURCE_KEY", "expanded-api-key")
	t.Setenv("API_SECURITY_LLM_KEY", "expanded-llm-key")
	raw := `
channels:
  - name: secure-channel
    type: discord
    webhook: https://secret.example/main
    options:
      webhook_plain: https://secret.example/plain
      webhooks:
        - https://secret.example/one
      nested:
        Authorization: ${API_SECURITY_AUTH}
        safe: visible
sources:
  - name: secure-source
    type: rss
    options:
      api_key: ${API_SECURITY_SOURCE_KEY}
      nested:
        password: expanded-password
        safe: visible
llm:
  api_key: ${API_SECURITY_LLM_KEY}
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := config.NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, 0)

	for _, endpoint := range []string{
		"/api/config",
		"/api/channels/secure-channel",
		"/api/sources/secure-source",
	} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		server.GetRouter().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, secret := range []string{
			"secret.example", "expanded-secret", "expanded-api-key",
			"expanded-password", "expanded-llm-key",
		} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s leaked %q in %s", endpoint, secret, body)
			}
		}
		if !strings.Contains(body, "visible") && endpoint != "/api/config" {
			t.Fatalf("%s removed safe nested data: %s", endpoint, body)
		}
	}
}

func TestManagementResponseRedactionPreservesDynamicSinkNames(t *testing.T) {
	value := map[string]interface{}{
		"sinks": map[string]interface{}{
			"discord-webhook": map[string]interface{}{"pending": float64(0)},
		},
		"webhook": "https://secret.example/hook",
	}

	redactResponseValue(value)
	if value["webhook"] != "***" {
		t.Fatalf("webhook secret was not redacted: %#v", value["webhook"])
	}
	sinks, ok := value["sinks"].(map[string]interface{})
	if !ok {
		t.Fatalf("sinks were unexpectedly redacted: %#v", value["sinks"])
	}
	if _, ok := sinks["discord-webhook"].(map[string]interface{}); !ok {
		t.Fatalf("dynamic sink name was unexpectedly redacted: %#v", sinks)
	}
}

func TestManagementResponseFlushesRecoveredPanicStatus(t *testing.T) {
	server := newConfigTestServer(t)
	server.router.GET("/api/config/panic", func(*gin.Context) {
		panic("management handler failed")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/config/panic", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%q", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != "0" {
		t.Fatalf("content-length=%q want=0 body=%q", got, rec.Body.String())
	}
}

func TestDeleteSourceRollsBackConfigWhenRuntimeRemovalFails(t *testing.T) {
	server := newConfigTestServer(t)
	server.removeSource = func(string) error {
		return core.ErrSourceConflict
	}

	assertConfigStatus(t, server, http.MethodDelete, "/api/sources/existing", "", http.StatusConflict)
	if _, ok := server.configManager.GetSourceConfig("existing"); !ok {
		t.Fatal("runtime failure left source deleted from config")
	}
}

func TestDeleteSourceReportsRuntimeAndRollbackFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "platform.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  - name: existing\n    type: rss\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := config.NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, 0)
	server.removeSource = func(string) error {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		return errors.New("generation changed")
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/sources/existing", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "restart_required") ||
		!strings.Contains(body, "generation changed") ||
		!strings.Contains(body, "config rollback failed") {
		t.Fatalf("compound failure was not reported clearly: %s", body)
	}
}
