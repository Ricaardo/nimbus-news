package runtime

import (
	"strings"
	"testing"
)

func TestDecodeFrameStrictness(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`
	if _, err := decodeFrame([]byte(valid)); err != nil {
		t.Fatalf("valid frame: %v", err)
	}
	for _, value := range []string{
		`{"jsonrpc":"2.0","jsonrpc":"2.0","id":"1","result":{}}`,
		`{"jsonrpc":"1.0","id":"1","result":{}}`,
		`{"jsonrpc":"2.0","id":"1","result":{},"extra":1}`,
		`{"jsonrpc":"2.0","id":"1","result":{},"error":{"code":-1,"message":"x","data":{"domain":"timeout","retryable":true}}}`,
		`{"jsonrpc":"2.0","id":"1","error":{"code":-1,"message":"x","data":{"domain":"timeout"}}}`,
		`{"jsonrpc":"2.0","id":null,"method":"health","params":{}}`,
		`{"jsonrpc":"2.0","id":true,"method":"health","params":{}}`,
		`{"jsonrpc":"2.0","id":"1","method":"health","params":null}`,
		`{"jsonrpc":"2.0","id":"1","result":{}} trailing`,
	} {
		if _, err := decodeFrame([]byte(value)); err == nil {
			t.Fatalf("invalid frame accepted: %s", value)
		}
	}
	if _, err := decodeFrame([]byte(strings.Repeat("x", MaxFrameBytes+1))); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestParseCommandRequiresJSONArrayWithoutShell(t *testing.T) {
	got, err := ParseCommand(`["bun","run","src/runtime/agent-runtime.ts"]`)
	if err != nil || strings.Join(got, "|") != "bun|run|src/runtime/agent-runtime.ts" {
		t.Fatalf("ParseCommand = %q, %v", got, err)
	}
	for _, value := range []string{"bun run child.ts", `[]`, `["bun",""]`, `["bun",1]`, `["bun"] trailing`} {
		if _, err := ParseCommand(value); err == nil {
			t.Fatalf("invalid command accepted: %q", value)
		}
	}
}
