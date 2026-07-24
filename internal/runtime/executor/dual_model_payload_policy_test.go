package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestDualModelPayloadPolicyClaudeWinsAfterThinking(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "sync"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seenBody = bytes.Clone(body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"glm-5.2","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			executor := NewClaudeExecutor(dualModelPayloadPolicyConfig())
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "test",
				"base_url": server.URL,
			}}
			request := cliproxyexecutor.Request{
				Model: "glm-5.2(low)",
				Payload: []byte(`{
					"model":"glm-5.2",
					"max_tokens":1024,
					"thinking":{"type":"adaptive"},
					"output_config":{"effort":"low"},
					"messages":[{"role":"user","content":"hi"}]
				}`),
			}
			options := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("claude"),
				Stream:       stream,
			}

			if stream {
				result, err := executor.ExecuteStream(context.Background(), auth, request, options)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else {
				if _, err := executor.Execute(context.Background(), auth, request, options); err != nil {
					t.Fatalf("Execute error: %v", err)
				}
			}

			assertGLMMaxPayload(t, seenBody)
		})
	}
}

func TestDualModelPayloadPolicyCodexWinsAfterThinking(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "sync"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seenBody = bytes.Clone(body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
			}))
			defer server.Close()

			executor := NewCodexExecutor(dualModelPayloadPolicyConfig())
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "test",
				"base_url": server.URL,
			}}
			request := cliproxyexecutor.Request{
				Model: "gpt-5.6-sol(low)",
				Payload: []byte(`{
					"model":"gpt-5.6-sol",
					"input":"hi",
					"reasoning":{"effort":"low"},
					"service_tier":"default"
				}`),
			}
			options := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       stream,
			}

			if stream {
				result, err := executor.ExecuteStream(context.Background(), auth, request, options)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else {
				if _, err := executor.Execute(context.Background(), auth, request, options); err != nil {
					t.Fatalf("Execute error: %v", err)
				}
			}

			assertGPTXHighPriorityPayload(t, seenBody)
		})
	}
}

func TestDualModelPayloadPolicyCodexCompact(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(dualModelPayloadPolicyConfig())
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test",
		"base_url": server.URL,
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.6-sol(low)",
		Payload: []byte(`{
			"model":"gpt-5.6-sol",
			"input":[{"type":"compaction_trigger"}],
			"reasoning":{"effort":"low"},
			"service_tier":"default"
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}

	assertGPTXHighPriorityPayload(t, seenBody)
}

func TestDualModelPayloadPolicyLeavesNonTargetModelUnchanged(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"other-model","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(dualModelPayloadPolicyConfig())
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test",
		"base_url": server.URL,
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "other-model",
		Payload: []byte(`{
			"model":"other-model",
			"input":"hi",
			"reasoning":{"effort":"low"},
			"service_tier":"default"
		}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(seenBody, "reasoning.effort").String(); got != "low" {
		t.Fatalf("reasoning.effort = %q, want low; body=%s", got, seenBody)
	}
	if got := gjson.GetBytes(seenBody, "service_tier").String(); got != "" {
		t.Fatalf("service_tier = %q, want translator default without target policy; body=%s", got, seenBody)
	}
}

func dualModelPayloadPolicyConfig() *config.Config {
	return &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "glm-5.2", Protocol: "claude"},
					},
					Params: map[string]any{
						"thinking.type":        "adaptive",
						"output_config.effort": "max",
					},
				},
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.6-sol", Protocol: "codex"},
						{Name: "gpt-5.6-sol", Protocol: "openai-response"},
					},
					Params: map[string]any{
						"reasoning.effort": "xhigh",
						"service_tier":     "priority",
					},
				},
			},
		},
	}
}

func assertGLMMaxPayload(t *testing.T, body []byte) {
	t.Helper()
	if got := gjson.GetBytes(body, "model").String(); got != "glm-5.2" {
		t.Fatalf("model = %q, want glm-5.2; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "output_config.effort").String(); got != "max" {
		t.Fatalf("output_config.effort = %q, want max; body=%s", got, body)
	}
}

func assertGPTXHighPriorityPayload(t *testing.T, body []byte) {
	t.Helper()
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q, want gpt-5.6-sol; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority; body=%s", got, body)
	}
}
