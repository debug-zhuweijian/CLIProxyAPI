package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelpolicy"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type modelPolicyPluginHost struct {
	handlerDirectExecutorRouteHost
	afterAuthBody   []byte
	afterAuthCalled bool
	executeCalls    int
	countCalls      int
	streamCalls     int
}

func (h *modelPolicyPluginHost) HasRequestInterceptors() bool {
	return len(h.afterAuthBody) > 0
}

func (*modelPolicyPluginHost) HasStreamInterceptors() bool { return false }

func (*modelPolicyPluginHost) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: cloneHeader(req.Headers), Body: cloneBytes(req.Body)}
}

func (h *modelPolicyPluginHost) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.afterAuthCalled = true
	body := h.afterAuthBody
	if len(body) == 0 {
		body = req.Body
	}
	return pluginapi.RequestInterceptResponse{Headers: cloneHeader(req.Headers), Body: cloneBytes(body)}
}

func (*modelPolicyPluginHost) InterceptResponse(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
}

func (*modelPolicyPluginHost) InterceptStreamChunk(_ context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
}

func (*modelPolicyPluginHost) PluginExecutorRequestToFormat(string, coreexecutor.Request, coreexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatCodex
}

func (h *modelPolicyPluginHost) ExecutePluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	h.executeCalls++
	return h.handlerDirectExecutorRouteHost.ExecutePluginExecutor(ctx, pluginID, req, opts)
}

func (h *modelPolicyPluginHost) CountPluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	h.countCalls++
	return h.handlerDirectExecutorRouteHost.CountPluginExecutor(ctx, pluginID, req, opts)
}

func (h *modelPolicyPluginHost) ExecutePluginExecutorStream(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	h.streamCalls++
	return h.handlerDirectExecutorRouteHost.ExecutePluginExecutorStream(ctx, pluginID, req, opts)
}

func configuredPluginPolicyHandler(host *modelPolicyPluginHost) *BaseAPIHandler {
	const (
		canonicalModel = "glm-5.2[1m]"
		upstreamModel  = "glm-5.2"
		targetPluginID = "model-policy-plugin"
	)
	host.hasRouters = true
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetExecutor,
			Target:     targetPluginID,
		}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ModelPolicy: sdkconfig.ModelPolicyConfig{
			Mode:             modelpolicy.ModeConfiguredOnly,
			CatalogAllowlist: []string{canonicalModel, "gpt-5.6-sol"},
			ProtocolRules: map[string][]sdkconfig.ModelPolicyRule{
				"openai": {
					{
						WireModel:             canonicalModel,
						Canonical:             canonicalModel,
						UpstreamModel:         upstreamModel,
						ExplicitContextSuffix: true,
					},
					{
						WireModel:     "gpt-5.6-sol",
						Canonical:     "gpt-5.6-sol",
						UpstreamModel: "gpt-5.6-sol",
					},
				},
			},
		},
	}, nil)
	handler.SetPluginHost(host)
	handler.SetModelRouterHost(host)
	return handler
}

func TestHandlerModelPolicyMapsPluginExecutorToApprovedUpstreamModel(t *testing.T) {
	const canonicalModel = "glm-5.2[1m]"
	host := &modelPolicyPluginHost{}
	handler := configuredPluginPolicyHandler(host)
	rawJSON := []byte(`{"model":"glm-5.2[1m]","input":"test"}`)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", canonicalModel, rawJSON, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "direct-ok" || host.executeCalls != 1 {
		t.Fatalf("execute result = %q calls=%d", body, host.executeCalls)
	}
	assertConfiguredPluginRequest(t, host, canonicalModel)

	host.lastRequest = coreexecutor.Request{}
	body, _, errMsg = handler.ExecuteCountWithAuthManager(context.Background(), "openai", canonicalModel, rawJSON, "")
	if errMsg != nil {
		t.Fatalf("ExecuteCountWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "7" || host.countCalls != 1 {
		t.Fatalf("count result = %q calls=%d", body, host.countCalls)
	}
	assertConfiguredPluginRequest(t, host, canonicalModel)

	host.lastRequest = coreexecutor.Request{}
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", canonicalModel, rawJSON, "")
	var chunks []byte
	for chunk := range dataChan {
		chunks = append(chunks, chunk...)
	}
	for streamErr := range errChan {
		if streamErr != nil {
			t.Fatalf("ExecuteStreamWithAuthManager() error = %+v", streamErr)
		}
	}
	if string(chunks) != "direct-stream" || host.streamCalls != 1 {
		t.Fatalf("stream result = %q calls=%d", chunks, host.streamCalls)
	}
	assertConfiguredPluginRequest(t, host, canonicalModel)
}

func TestHandlerModelPolicyRejectsPluginAfterAuthModelMutation(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		execute func(*BaseAPIHandler) bool
	}{
		{
			name:  "execute unconfigured",
			model: "unexpected-model",
			execute: func(handler *BaseAPIHandler) bool {
				_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", "glm-5.2[1m]", []byte(`{"model":"glm-5.2[1m]"}`), "")
				return errMsg != nil
			},
		},
		{
			name:  "count unconfigured",
			model: "unexpected-model",
			execute: func(handler *BaseAPIHandler) bool {
				_, _, errMsg := handler.ExecuteCountWithAuthManager(context.Background(), "openai", "glm-5.2[1m]", []byte(`{"model":"glm-5.2[1m]"}`), "")
				return errMsg != nil
			},
		},
		{
			name:  "stream configured cross-canonical",
			model: "gpt-5.6-sol",
			execute: func(handler *BaseAPIHandler) bool {
				dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "glm-5.2[1m]", []byte(`{"model":"glm-5.2[1m]"}`), "")
				if dataChan != nil {
					for range dataChan {
					}
				}
				for errMsg := range errChan {
					if errMsg != nil {
						return true
					}
				}
				return false
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &modelPolicyPluginHost{
				afterAuthBody: []byte(fmt.Sprintf(`{"model":%q,"input":"after"}`, tt.model)),
			}
			handler := configuredPluginPolicyHandler(host)
			if !tt.execute(handler) {
				t.Fatal("model policy accepted a plugin after-auth model mutation")
			}
			if !host.afterAuthCalled {
				t.Fatal("after-auth interceptor was not called")
			}
			if host.executeCalls != 0 || host.countCalls != 0 || host.streamCalls != 0 {
				t.Fatalf("model mutation reached plugin executor: execute=%d count=%d stream=%d", host.executeCalls, host.countCalls, host.streamCalls)
			}
		})
	}
}

func TestHandlerModelPolicyAllowsPluginAfterAuthNonModelMutation(t *testing.T) {
	host := &modelPolicyPluginHost{
		afterAuthBody: []byte(`{"model":"glm-5.2","input":"after"}`),
	}
	handler := configuredPluginPolicyHandler(host)
	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", "glm-5.2[1m]", []byte(`{"model":"glm-5.2[1m]","input":"before"}`), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "direct-ok" || host.executeCalls != 1 {
		t.Fatalf("execute result = %q calls=%d", body, host.executeCalls)
	}
	if got := string(host.lastRequest.Payload); got != `{"model":"glm-5.2","input":"after"}` {
		t.Fatalf("plugin executor payload = %q", got)
	}
}

func assertConfiguredPluginRequest(t *testing.T, host *modelPolicyPluginHost, canonicalModel string) {
	t.Helper()
	if host.lastRequest.Model != "glm-5.2" {
		t.Fatalf("executor model = %q, want glm-5.2", host.lastRequest.Model)
	}
	if got := string(host.lastRequest.Payload); got != `{"model":"glm-5.2","input":"test"}` {
		t.Fatalf("executor payload = %q", got)
	}
	if got := host.lastOptions.Metadata[modelpolicy.CanonicalModelMetadataKey]; got != canonicalModel {
		t.Fatalf("canonical metadata = %#v, want %q", got, canonicalModel)
	}
	if got := host.lastOptions.Metadata[modelpolicy.ContextModeMetadataKey]; got != "1m" {
		t.Fatalf("context metadata = %#v, want 1m", got)
	}
	if got, _ := host.lastOptions.Metadata[modelpolicy.ApprovalHashMetadataKey].(string); got == "" {
		t.Fatal("approval hash metadata is empty")
	}
}
