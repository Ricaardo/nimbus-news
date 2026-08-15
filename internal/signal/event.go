package signal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

const Version = 1

const (
	TypeMacro         = "macro"
	TypeShortInterest = "short_interest"
	Type13F           = "13f"
	TypeAnalystRating = "analyst_rating"
)

type Provenance struct {
	RawHash     string `json:"raw_hash"`
	RetrievedAt string `json:"retrieved_at"`
	RawURL      string `json:"raw_url,omitempty"`
}

type Event struct {
	Version    int             `json:"version"`
	SignalID   string          `json:"signal_id"`
	Type       string          `json:"type"`
	AsOf       string          `json:"as_of"`
	Symbols    []string        `json:"symbols"`
	Source     string          `json:"source"`
	SourceID   string          `json:"source_id"`
	Facts      json.RawMessage `json:"facts"`
	Provenance Provenance      `json:"provenance"`
}

func (e Event) Validate() error {
	if e.Version != Version {
		return fmt.Errorf("signal: unsupported version %d", e.Version)
	}
	if e.SignalID == "" || e.Type == "" || e.AsOf == "" || e.Source == "" || len(e.Facts) == 0 {
		return fmt.Errorf("signal: signal_id, type, as_of, source and facts are required")
	}
	var facts map[string]json.RawMessage
	if err := json.Unmarshal(e.Facts, &facts); err != nil || facts == nil {
		return fmt.Errorf("signal: facts must be a JSON object")
	}
	return nil
}

// StableID exactly matches services/signal-gateway signal_id(): Python
// json.dumps(sort_keys=True, ensure_ascii=False, default=str), including spaces
// and number lexemes such as 1 versus 1.0.
func StableID(signalType, asOf string, symbols []string, source string, facts json.RawMessage) (string, error) {
	canonicalFacts, err := pythonCanonicalJSON(facts)
	if err != nil {
		return "", fmt.Errorf("signal: canonicalize facts: %w", err)
	}
	var b strings.Builder
	b.WriteString(`{"as_of": `)
	b.WriteString(pythonString(asOf))
	b.WriteString(`, "facts": `)
	b.Write(canonicalFacts)
	b.WriteString(`, "source": `)
	b.WriteString(pythonString(source))
	b.WriteString(`, "symbols": [`)
	for i, symbol := range symbols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pythonString(symbol))
	}
	b.WriteString(`], "type": `)
	b.WriteString(pythonString(signalType))
	b.WriteByte('}')
	sum := sha256.Sum256([]byte(b.String()))
	return signalType + ":" + asOf + ":" + hex.EncodeToString(sum[:])[:16], nil
}

func pythonCanonicalJSON(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, fmt.Errorf("multiple JSON values")
	}
	var b strings.Builder
	if err := writePythonJSON(&b, value); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func writePythonJSON(b *strings.Builder, value any) error {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		b.WriteString(pythonString(v))
	case json.Number:
		number, err := pythonNumber(v.String())
		if err != nil {
			return err
		}
		b.WriteString(number)
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := writePythonJSON(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pythonString(key))
			b.WriteString(": ")
			if err := writePythonJSON(b, v[key]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func pythonNumber(value string) (string, error) {
	if !strings.ContainsAny(value, ".eE") {
		integer := new(big.Int)
		if _, ok := integer.SetString(value, 10); !ok {
			return "", fmt.Errorf("invalid integer %q", value)
		}
		return integer.String(), nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return "", fmt.Errorf("invalid float %q", value)
	}
	scientific := strconv.FormatFloat(number, 'e', -1, 64)
	parts := strings.Split(scientific, "e")
	exponent, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", err
	}
	if exponent < -4 || exponent >= 16 {
		return scientific, nil
	}
	negative := strings.HasPrefix(parts[0], "-")
	mantissa := strings.TrimPrefix(parts[0], "-")
	digits := strings.ReplaceAll(mantissa, ".", "")
	decimal := exponent + 1
	var fixed string
	switch {
	case decimal <= 0:
		fixed = "0." + strings.Repeat("0", -decimal) + digits
	case decimal >= len(digits):
		fixed = digits + strings.Repeat("0", decimal-len(digits)) + ".0"
	default:
		fixed = digits[:decimal] + "." + digits[decimal:]
	}
	if negative {
		fixed = "-" + fixed
	}
	return fixed, nil
}

func pythonString(value string) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	encoded := strings.TrimSuffix(b.String(), "\n")
	encoded = strings.ReplaceAll(encoded, `\u2028`, " ")
	encoded = strings.ReplaceAll(encoded, `\u2029`, " ")
	return encoded
}
