package signal

import (
	"encoding/json"
	"net/http"
	"strings"
)

func NewHandler(store QueryStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "shadow", "fetch": false})
	})
	mux.HandleFunc("GET /query", func(w http.ResponseWriter, r *http.Request) {
		query := Query{
			Type: r.URL.Query().Get("type"), Symbol: r.URL.Query().Get("symbol"), AsOf: r.URL.Query().Get("as_of"),
			Latest: boolParam(r.URL.Query().Get("latest")),
		}
		events, err := store.Query(r.Context(), query)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, events)
	})
	mux.HandleFunc("GET /render/{target}", func(w http.ResponseWriter, r *http.Request) {
		target := RenderTarget(r.PathValue("target"))
		if !validRenderTarget(target) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown render target: " + string(target)})
			return
		}
		query := Query{Type: r.URL.Query().Get("type"), Symbol: r.URL.Query().Get("symbol"), Latest: boolParam(r.URL.Query().Get("latest"))}
		events, err := store.Query(r.Context(), query)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		values := make([]any, 0, len(events))
		for _, event := range events {
			value, err := Render(event, target)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			values = append(values, value)
		}
		if target == RenderNimbusSkill {
			parts := make([]string, len(values))
			for i := range values {
				parts[i] = values[i].(string)
			}
			writeJSON(w, http.StatusOK, strings.Join(parts, "\n\n---\n\n"))
			return
		}
		writeJSON(w, http.StatusOK, values)
	})
	return mux
}

func validRenderTarget(target RenderTarget) bool {
	return target == RenderNewsPush || target == RenderScreenerFactor || target == RenderNimbusSkill
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func boolParam(value string) bool {
	value = strings.ToLower(value)
	return value == "1" || value == "true" || value == "yes"
}
