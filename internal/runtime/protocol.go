// Package runtime supervises optional NDJSON JSON-RPC capability children.
package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 262144
)

type Domain string

const (
	DomainTimeout          Domain = "timeout"
	DomainCancelled        Domain = "cancelled"
	DomainDependencyDown   Domain = "dependency_down"
	DomainOverloaded       Domain = "overloaded"
	DomainChildCrashed     Domain = "child_crashed"
	DomainPermissionDenied Domain = "permission_denied"
	DomainUnsafeRequest    Domain = "unsafe_request"
	DomainProtocolMismatch Domain = "protocol_mismatch"
)

type ErrorData struct {
	Domain    Domain `json:"domain"`
	Retryable bool   `json:"retryable"`
	retrySet  bool
}

func (d ErrorData) MarshalJSON() ([]byte, error) {
	type wire struct {
		Domain    Domain `json:"domain"`
		Retryable bool   `json:"retryable"`
	}
	return json.Marshal(wire{Domain: d.Domain, Retryable: d.Retryable})
}

func (d *ErrorData) UnmarshalJSON(data []byte) error {
	var value struct {
		Domain    Domain `json:"domain"`
		Retryable *bool  `json:"retryable"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if value.Retryable == nil {
		return errors.New("retryable is required")
	}
	d.Domain = value.Domain
	d.Retryable = *value.Retryable
	d.retrySet = true
	return nil
}

type RPCError struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    ErrorData `json:"data"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("runtime %s: %s", e.Data.Domain, e.Message)
}

func NewError(domain Domain, retryable bool, message string) *RPCError {
	return &RPCError{Code: -32000, Message: sanitizeDiagnostic(message, nil), Data: ErrorData{Domain: domain, Retryable: retryable, retrySet: true}}
}

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (f frame) request() bool { return f.Method != "" && len(f.ID) > 0 }
func (f frame) response() bool {
	return f.Method == "" && len(f.ID) > 0 && (len(f.Result) > 0 || f.Error != nil)
}

func decodeFrame(data []byte) (frame, error) {
	if len(data) == 0 || len(data) > MaxFrameBytes {
		return frame{}, fmt.Errorf("frame size %d is invalid", len(data))
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return frame{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var value frame
	if err := decoder.Decode(&value); err != nil {
		return frame{}, err
	}
	if err := ensureEOF(decoder); err != nil {
		return frame{}, err
	}
	if value.JSONRPC != "2.0" {
		return frame{}, errors.New("jsonrpc must equal 2.0")
	}
	if value.Method != "" {
		if len(value.Result) > 0 || value.Error != nil {
			return frame{}, errors.New("request cannot contain result or error")
		}
		if len(value.Params) == 0 {
			value.Params = json.RawMessage(`{}`)
		}
		if !jsonObject(value.Params) {
			return frame{}, errors.New("params must be an object")
		}
		if len(value.ID) > 0 && !validID(value.ID) {
			return frame{}, errors.New("invalid request id")
		}
		return value, nil
	}
	if !value.response() || !validID(value.ID) || (len(value.Result) > 0) == (value.Error != nil) {
		return frame{}, errors.New("invalid response frame")
	}
	if value.Error != nil {
		if value.Error.Message == "" || !validDomain(value.Error.Data.Domain) || !value.Error.Data.retrySet {
			return frame{}, errors.New("invalid structured runtime error")
		}
		value.Error.Message = sanitizeDiagnostic(value.Error.Message, nil)
	}
	return value, nil
}

func jsonObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func validID(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch id := value.(type) {
	case string:
		return true
	case json.Number:
		_, err := id.Int64()
		return err == nil
	default:
		return false
	}
}

func validDomain(domain Domain) bool {
	switch domain {
	case DomainTimeout, DomainCancelled, DomainDependencyDown, DomainOverloaded,
		DomainChildCrashed, DomainPermissionDenied, DomainUnsafeRequest, DomainProtocolMismatch:
		return true
	default:
		return false
	}
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return scanJSONValue(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
}
