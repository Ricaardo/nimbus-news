package signal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type RenderTarget string

const (
	RenderNewsPush       RenderTarget = "news_push"
	RenderScreenerFactor RenderTarget = "screener_factor"
	RenderNimbusSkill    RenderTarget = "nimbus_skill"
)

func Render(event Event, target RenderTarget) (any, error) {
	var facts map[string]any
	decoder := json.NewDecoder(bytes.NewReader(event.Facts))
	decoder.UseNumber()
	if err := decoder.Decode(&facts); err != nil {
		return nil, err
	}
	switch target {
	case RenderNewsPush:
		return renderNewsPush(event, facts), nil
	case RenderScreenerFactor:
		return renderScreenerFactor(event, facts), nil
	case RenderNimbusSkill:
		return renderNimbusSkill(event, facts), nil
	default:
		return nil, fmt.Errorf("signal: unknown render target %q", target)
	}
}

func renderNewsPush(event Event, facts map[string]any) map[string]any {
	symbols := strings.Join(event.Symbols, ",")
	var text string
	switch event.Type {
	case TypeShortInterest:
		obs := firstObservation(facts["observations"])
		ratio := pythonOr(obs["shortRatio"], obs["short_ratio"])
		text = fmt.Sprintf("[空头] %s as_of %s: short ratio=%v, %%float=%v", symbols, event.AsOf, pythonDisplay(ratio), pythonDisplay(obs["shortPercentOfFloat"]))
	case TypeMacro:
		text = fmt.Sprintf("[宏观] %v as_of %s: %s", pythonDisplay(facts["indicator"]), event.AsOf, pythonObservationRepr(event.Facts))
	case Type13F:
		institution := pythonOr(facts["institution"], facts["cik"])
		text = fmt.Sprintf("[13F] %v as_of %s: %d holdings", pythonDisplay(institution), event.AsOf, observationCount(facts["observations"]))
	case TypeAnalystRating:
		text = fmt.Sprintf("[评级] %s as_of %s: %d targets", symbols, event.AsOf, observationCount(facts["observations"]))
	default:
		text = fmt.Sprintf("[%s] %s as_of %s", event.Type, symbols, event.AsOf)
	}
	return map[string]any{"text": text, "symbols": event.Symbols, "event_id": event.SignalID}
}

func renderScreenerFactor(event Event, facts map[string]any) map[string]any {
	var symbol any
	if len(event.Symbols) > 0 {
		symbol = event.Symbols[0]
	}
	out := map[string]any{"symbol": symbol, "as_of": event.AsOf, "type": event.Type}
	switch event.Type {
	case TypeShortInterest:
		obs := firstObservation(facts["observations"])
		out["short_ratio"] = pythonOr(obs["shortRatio"], obs["short_ratio"])
		out["short_pct_float"] = obs["shortPercentOfFloat"]
		out["shares_short"] = pythonOr(obs["sharesShort"], obs["shares_short"])
	case TypeMacro:
		out["indicator"] = facts["indicator"]
		out["value"] = firstMacroNumeric(event.Facts)
	}
	return out
}

func renderNimbusSkill(event Event, facts map[string]any) string {
	symbols := strings.Join(event.Symbols, ", ")
	if symbols == "" {
		symbols = "—"
	}
	hash := event.Provenance.RawHash
	if len(hash) > 18 {
		hash = hash[:18]
	}
	lines := []string{
		fmt.Sprintf("**Signal** `%s`  ·  symbols: %s  ·  as_of: %s", event.Type, symbols, event.AsOf),
		fmt.Sprintf("source: `%s`  ·  id: `%s`", event.Source, event.SignalID),
		fmt.Sprintf("provenance: retrieved_at=%s hash=%s…", event.Provenance.RetrievedAt, hash),
	}
	if observations, ok := facts["observations"].([]any); ok {
		lines = append(lines, fmt.Sprintf("observations: %d row(s)", len(observations)))
	}
	return strings.Join(lines, "\n")
}

func firstObservation(value any) map[string]any {
	switch observations := value.(type) {
	case []any:
		if len(observations) > 0 {
			if row, ok := observations[0].(map[string]any); ok {
				return row
			}
		}
	case map[string]any:
		return observations
	}
	return map[string]any{}
}

func observationCount(value any) int {
	if rows, ok := value.([]any); ok {
		return len(rows)
	}
	return 0
}

func pythonOr(first, second any) any {
	if pythonTruthy(first) {
		return first
	}
	return second
}
func pythonTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, _ := v.Float64()
		return f != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}
func pythonDisplay(value any) any {
	if value == nil {
		return "None"
	}
	return value
}
func firstMacroNumeric(facts json.RawMessage) any {
	var wrapper struct {
		Observations json.RawMessage `json:"observations"`
	}
	if json.Unmarshal(facts, &wrapper) != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(wrapper.Observations))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	if delimiter, ok := token.(json.Delim); ok && delimiter == '[' {
		if !decoder.More() {
			return nil
		}
		token, err = decoder.Token()
		if err != nil {
			return nil
		}
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil
		}
		if key.(string) != "date" {
			if number, ok := value.(json.Number); ok {
				return number
			}
		}
	}
	return nil
}

func pythonObservationRepr(facts json.RawMessage) string {
	var wrapper struct {
		Observations json.RawMessage `json:"observations"`
	}
	if json.Unmarshal(facts, &wrapper) != nil || len(wrapper.Observations) == 0 {
		return "{}"
	}
	decoder := json.NewDecoder(bytes.NewReader(wrapper.Observations))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return "{}"
	}
	if delimiter, ok := token.(json.Delim); ok && delimiter == '[' {
		if !decoder.More() {
			return "{}"
		}
		var b strings.Builder
		if writePythonRepr(&b, decoder) != nil {
			return "{}"
		}
		return b.String()
	}
	decoder = json.NewDecoder(bytes.NewReader(wrapper.Observations))
	decoder.UseNumber()
	var b strings.Builder
	if writePythonRepr(&b, decoder) != nil {
		return "{}"
	}
	return b.String()
}

func writePythonRepr(b *strings.Builder, decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			b.WriteByte('{')
			first := true
			for decoder.More() {
				if !first {
					b.WriteString(", ")
				}
				first = false
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				b.WriteString(pythonReprString(key.(string)))
				b.WriteString(": ")
				if err := writePythonRepr(b, decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			b.WriteByte('}')
			return err
		case '[':
			b.WriteByte('[')
			first := true
			for decoder.More() {
				if !first {
					b.WriteString(", ")
				}
				first = false
				if err := writePythonRepr(b, decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			b.WriteByte(']')
			return err
		}
	case string:
		b.WriteString(pythonReprString(value))
	case json.Number:
		number, err := pythonNumber(value.String())
		if err != nil {
			return err
		}
		b.WriteString(number)
	case bool:
		if value {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case nil:
		b.WriteString("None")
	}
	return nil
}

func pythonReprString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\r", `\r`)
	value = strings.ReplaceAll(value, "\t", `\t`)
	return "'" + value + "'"
}
