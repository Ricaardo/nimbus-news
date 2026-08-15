package warehouse

import (
	"encoding/json"
	"net/http"
)

type CandidateStatus struct {
	Enabled         bool   `json:"enabled"`
	Prepared        bool   `json:"prepared"`
	Serving         bool   `json:"serving"`
	ProductionOwner bool   `json:"production_owner"`
	Root            string `json:"root,omitempty"`
	Manifest        string `json:"manifest,omitempty"`
	ManifestSHA256  string `json:"manifest_sha256,omitempty"`
	Error           string `json:"error,omitempty"`
}

func InspectCandidate(root, manifest string) CandidateStatus {
	status := CandidateStatus{Enabled: root != "" || manifest != "", Root: root, Manifest: manifest}
	if !status.Enabled {
		return status
	}
	if root == "" || manifest == "" {
		status.Error = "warehouse root and manifest must be configured together"
		return status
	}
	validated, err := ValidateStaging(manifest)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Prepared = true
	status.ManifestSHA256 = validated.ManifestHash
	return status
}

func StatusHandler(status CandidateStatus) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(status)
	})
}
