package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func chatEvent(delta map[string]any, finish any) string {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
	return "data: " + string(b) + "\n\n"
}

func toolDelta(index int, id, name, args string) map[string]any {
	return map[string]any{"index": index, "id": id, "function": map[string]any{"name": name, "arguments": args}}
}

func runAnthropicStream(t *testing.T, reader io.Reader) (string, RequestLog) {
	t.Helper()
	oldPath, oldLogs := requestLogsPath, requestLogs
	requestLogsPath, requestLogs = t.TempDir()+"/logs.json", nil
	t.Cleanup(func() { requestLogsPath, requestLogs = oldPath, oldLogs })
	w := httptest.NewRecorder()
	entry := RequestLog{ID: "stream-test", StartedAt: time.Now(), Model: freeModelPrimary}
	handleAnthropicStream(w, &http.Response{Body: io.NopCloser(reader)}, nil, &entry)
	return w.Body.String(), entry
}

func TestAnthropicStreamPreservesInterleavedToolArguments(t *testing.T) {
	input := chatEvent(map[string]any{"content": "Checking files."}, nil) +
		chatEvent(map[string]any{"tool_calls": []any{toolDelta(0, "call_1", "read_file", `{"path":`), toolDelta(1, "call_2", "read_file", `{"path":"b`)}}, nil) +
		chatEvent(map[string]any{"tool_calls": []any{toolDelta(1, "", "", `.go"}`), toolDelta(0, "", "", `"a.go"}`)}}, "tool_calls") + "data: [DONE]" // No final newline.
	output, entry := runAnthropicStream(t, strings.NewReader(input))
	starts, stops, args := map[int]string{}, map[int]int{}, map[int]string{}
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		idx, _ := event["index"].(float64)
		switch event["type"] {
		case "content_block_start":
			block := event["content_block"].(map[string]any)
			if _, exists := starts[int(idx)]; exists {
				t.Fatal("reused content block index")
			}
			starts[int(idx)], _ = block["type"].(string)
		case "content_block_stop":
			stops[int(idx)]++
		case "content_block_delta":
			delta := event["delta"].(map[string]any)
			if value, ok := delta["partial_json"].(string); ok {
				args[int(idx)] += value
			}
		}
	}
	if starts[0] != "text" || starts[1] != "tool_use" || starts[2] != "tool_use" || len(starts) != 3 {
		t.Fatalf("unexpected content blocks: %v", starts)
	}
	for idx, expected := range map[int]string{1: `{"path":"a.go"}`, 2: `{"path":"b.go"}`} {
		if args[idx] != expected || stops[idx] != 1 {
			t.Fatalf("tool %d lost fragments: %q", idx, args[idx])
		}
	}
	if !entry.Completed || !strings.Contains(output, "event: message_stop") {
		t.Fatal("valid stream not completed")
	}
}

type brokenStreamReader struct{}

func (brokenStreamReader) Read([]byte) (int, error) { return 0, errors.New("upstream connection lost") }

func TestAnthropicStreamFailuresDoNotReportSuccess(t *testing.T) {
	for name, reader := range map[string]io.Reader{
		"network":        io.MultiReader(strings.NewReader(chatEvent(map[string]any{"content": "partial"}, nil)), brokenStreamReader{}),
		"truncated":      strings.NewReader(chatEvent(map[string]any{"content": "partial"}, nil)),
		"tool_json":      strings.NewReader(chatEvent(map[string]any{"tool_calls": []any{toolDelta(0, "call", "tool", `{"path":`)}}, "tool_calls") + "data: [DONE]\n\n"),
		"upstream_error": strings.NewReader("data: {\"error\":{\"message\":\"quota\"}}\n\n"),
	} {
		t.Run(name, func(t *testing.T) {
			output, entry := runAnthropicStream(t, reader)
			if entry.Completed || entry.Error == "" || !strings.Contains(output, "event: error") || strings.Contains(output, "event: message_stop") {
				t.Fatalf("failure treated as success: %+v", entry)
			}
		})
	}
}

func TestAnthropicNonStreamPreservesToolCalls(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Accounts: []*Account{groupedAccount("one", "free")}}
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":"tool_calls"}]}`))}, nil
	})
	w := jsonRecorder(handleAnthropicMessages, `{"model":"`+freeModelPrimary+`","max_tokens":32,"messages":[{"role":"user","content":"read file"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"tool_use"`) || !strings.Contains(w.Body.String(), `"path":"a.go"`) {
		t.Fatalf("tool output lost: %s", w.Body.String())
	}
}
