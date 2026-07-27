package claude

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelpolicy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestClaudeModelsResponseUsesConfiguredDisplayName(t *testing.T) {
	const clientID = "claude-display-name-catalog-test"
	const modelID = "claude-display-name-catalog-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: "test", DisplayName: "Configured Claude Name",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).ClaudeModels(ctx)

	var response struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model.ID == modelID {
			if model.DisplayName != "Configured Claude Name" {
				t.Fatalf("display_name = %q, want Configured Claude Name", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", modelID)
}

func TestClaudeModelsUsesConfiguredOnlyCatalog(t *testing.T) {
	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{
		Cfg: &sdkconfig.SDKConfig{
			ModelPolicy: sdkconfig.ModelPolicyConfig{
				Mode:             modelpolicy.ModeConfiguredOnly,
				CatalogAllowlist: []string{"glm-5.2[1m]", "gpt-5.6-sol"},
				ProtocolRules: map[string][]sdkconfig.ModelPolicyRule{
					"anthropic": {{
						WireModel:     "glm-5.2[1m]",
						Canonical:     "glm-5.2[1m]",
						UpstreamModel: "glm-5.2",
					}},
					"openai-response": {{
						WireModel:     "gpt-5.6-sol",
						Canonical:     "gpt-5.6-sol",
						UpstreamModel: "gpt-5.6-sol",
					}},
				},
			},
		},
	})

	models := handler.Models()
	if len(models) != 1 {
		t.Fatalf("Models() returned %d models, want only the Anthropic-configured model: %#v", len(models), models)
	}
	if got := models[0]["id"]; got != "glm-5.2[1m]" {
		t.Fatalf("Models()[0].id = %#v, want glm-5.2[1m]", got)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler.ClaudeModels(ctx)
	if got := gjson.GetBytes(recorder.Body.Bytes(), "data.0.id").String(); got != "claude-fable-5-dd-]m1[2.5-mlg" {
		t.Fatalf("ClaudeModels encoded id = %q", got)
	}
	if gjson.GetBytes(recorder.Body.Bytes(), "data.#").Int() != 1 {
		t.Fatalf("ClaudeModels response = %s", recorder.Body.String())
	}
}

func TestRewriteClaudeDDModelInBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantModel string
	}{
		{
			name:      "encoded model is decoded",
			body:      `{"model":"claude-fable-5-dd-o4-tpg","messages":[]}`,
			wantModel: "gpt-4o",
		},
		{
			name:      "plain claude model unchanged",
			body:      `{"model":"claude-sonnet-4-6","messages":[]}`,
			wantModel: "claude-sonnet-4-6",
		},
		{
			name:      "encoded model with thinking suffix",
			body:      `{"model":"claude-fable-5-dd-o4-tpg(high)","stream":true}`,
			wantModel: "gpt-4o(high)",
		},
		{
			name:      "missing model field unchanged",
			body:      `{"messages":[]}`,
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rewriteClaudeDDModelInBody([]byte(tt.body))
			if err != nil {
				t.Fatalf("rewriteClaudeDDModelInBody() error = %v", err)
			}
			if model := gjson.GetBytes(got, "model").String(); model != tt.wantModel {
				t.Fatalf("model = %q, want %q; body=%s", model, tt.wantModel, string(got))
			}
		})
	}
}

func TestRewriteClaudeDDModelInBodyRejectsMalformedFableID(t *testing.T) {
	if _, err := rewriteClaudeDDModelInBody([]byte(`{"model":"claude-fable-5-dd-"}`)); err == nil {
		t.Fatal("rewriteClaudeDDModelInBody accepted malformed fable ID")
	}
}
