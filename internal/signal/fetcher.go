package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type FetchKind string

const (
	FetchMacro         FetchKind = "macro"
	FetchShortInterest FetchKind = "short-interest"
	Fetch13F           FetchKind = "13f"
	FetchAnalystRating FetchKind = "analyst-rating"
)

type FetchRequest struct {
	Kind   FetchKind
	Params url.Values
}

type Fetcher interface {
	Fetch(context.Context, FetchRequest) (Event, error)
}

// HTTPFetcher is a compatibility client for the legacy Python gateway. It is
// opt-in and is not constructed by the shadow candidate.
type HTTPFetcher struct {
	BaseURL string
	Client  *http.Client
}

func (f HTTPFetcher) Fetch(ctx context.Context, request FetchRequest) (Event, error) {
	switch request.Kind {
	case FetchMacro, FetchShortInterest, Fetch13F, FetchAnalystRating:
	default:
		return Event{}, fmt.Errorf("signal: unsupported fetch kind %q", request.Kind)
	}
	base := strings.TrimRight(f.BaseURL, "/")
	if base == "" {
		return Event{}, fmt.Errorf("signal: fetch base URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/fetch/"+string(request.Kind)+"?"+request.Params.Encode(), nil)
	if err != nil {
		return Event{}, err
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return Event{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Event{}, fmt.Errorf("signal: fetch returned %s", response.Status)
	}
	var event Event
	if err := json.NewDecoder(response.Body).Decode(&event); err != nil {
		return Event{}, err
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}
