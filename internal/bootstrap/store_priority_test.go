package bootstrap

import (
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
)

func TestDigestSourcePrioritiesComeFromRoutingConfig(t *testing.T) {
	cfg := &config.PlatformConfig{Sources: []config.SourceConfig{
		{Name: "known", Routing: &config.SourceRoutingConfig{Digest: &config.RouteBand{Priority: 50}}},
		{Name: "direct-only", Routing: &config.SourceRoutingConfig{Direct: &config.RouteBand{Priority: 40}}},
		{Name: "legacy"},
	}}
	priorities := digestSourcePriorities(cfg)
	if priorities["known"] != 50 || priorities["direct-only"] != 0 || priorities["legacy"] != 0 {
		t.Fatalf("priorities=%v", priorities)
	}
}
