package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func readPlatformFile(path string) (*PlatformConfig, *PlatformConfig, string, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("failed to open config file: %w", err)
	}
	raw, live, err := parsePlatformBytes(data)
	if err != nil {
		return nil, nil, "", nil, err
	}
	return raw, live, fingerprintBytes(data), data, nil
}

func parsePlatformBytes(data []byte) (*PlatformConfig, *PlatformConfig, error) {
	var raw PlatformConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("failed to decode config file: %w", err)
	}
	raw.setDefaults()
	if err := raw.Validate(); err != nil {
		return nil, nil, err
	}
	live, err := clonePlatformConfig(&raw)
	if err != nil {
		return nil, nil, err
	}
	live.expandEnvVars()
	if err := live.Validate(); err != nil {
		return nil, nil, err
	}
	return &raw, live, nil
}

func fingerprintBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func currentFileFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fingerprintBytes(data), nil
}

func preserveEnvironmentReferences(next, raw, live *PlatformConfig) {
	if next == nil || raw == nil || live == nil {
		return
	}
	next.LLM.APIURL = preserveEnvString(next.LLM.APIURL, raw.LLM.APIURL, live.LLM.APIURL)
	next.LLM.APIKey = preserveEnvString(next.LLM.APIKey, raw.LLM.APIKey, live.LLM.APIKey)

	rawChannels := make(map[string]ChannelConfig, len(raw.Channels))
	liveChannels := make(map[string]ChannelConfig, len(live.Channels))
	for _, channel := range raw.Channels {
		rawChannels[channel.Name] = channel
	}
	for _, channel := range live.Channels {
		liveChannels[channel.Name] = channel
	}
	for i := range next.Channels {
		rawChannel, rawOK := rawChannels[next.Channels[i].Name]
		liveChannel, liveOK := liveChannels[next.Channels[i].Name]
		if !rawOK || !liveOK {
			continue
		}
		next.Channels[i].Webhook = preserveEnvString(next.Channels[i].Webhook, rawChannel.Webhook, liveChannel.Webhook)
		next.Channels[i].Options = preserveEnvMap(next.Channels[i].Options, rawChannel.Options, liveChannel.Options)
	}

	rawSources := make(map[string]SourceConfig, len(raw.Sources))
	liveSources := make(map[string]SourceConfig, len(live.Sources))
	for _, source := range raw.Sources {
		rawSources[source.Name] = source
	}
	for _, source := range live.Sources {
		liveSources[source.Name] = source
	}
	for i := range next.Sources {
		rawSource, rawOK := rawSources[next.Sources[i].Name]
		liveSource, liveOK := liveSources[next.Sources[i].Name]
		if !rawOK || !liveOK {
			continue
		}
		next.Sources[i].URL = preserveEnvString(next.Sources[i].URL, rawSource.URL, liveSource.URL)
		next.Sources[i].Options = preserveEnvMap(next.Sources[i].Options, rawSource.Options, liveSource.Options)
	}
}

func preserveEnvMap(next, raw, live map[string]interface{}) map[string]interface{} {
	for key, value := range next {
		rawValue, rawOK := raw[key]
		liveValue, liveOK := live[key]
		if rawOK && liveOK {
			next[key] = preserveEnvValue(value, rawValue, liveValue)
		}
	}
	return next
}

func preserveEnvValue(next, raw, live interface{}) interface{} {
	switch value := next.(type) {
	case string:
		rawString, rawOK := raw.(string)
		liveString, liveOK := live.(string)
		if rawOK && liveOK {
			return preserveEnvString(value, rawString, liveString)
		}
	case []interface{}:
		rawSlice, rawOK := raw.([]interface{})
		liveSlice, liveOK := live.([]interface{})
		if !rawOK || !liveOK {
			return next
		}
		for i := range value {
			if i < len(rawSlice) && i < len(liveSlice) {
				value[i] = preserveEnvValue(value[i], rawSlice[i], liveSlice[i])
			}
		}
	case map[string]interface{}:
		rawMap, rawOK := raw.(map[string]interface{})
		liveMap, liveOK := live.(map[string]interface{})
		if rawOK && liveOK {
			return preserveEnvMap(value, rawMap, liveMap)
		}
	}
	return next
}

func preserveEnvString(next, raw, live string) string {
	if strings.Contains(raw, "${") && next == live {
		return raw
	}
	return next
}
