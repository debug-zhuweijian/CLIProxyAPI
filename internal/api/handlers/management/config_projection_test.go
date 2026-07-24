package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func testConfiguredModelPolicy() config.ModelPolicyConfig {
	return config.ModelPolicyConfig{
		Mode:             "configured-only",
		CatalogAllowlist: []string{"glm-5.2[1m]", "gpt-5.6-sol"},
		ProtocolRules: map[string][]config.ModelPolicyRule{
			"anthropic": {
				{
					WireModel:     "glm-5.2[1m]",
					Canonical:     "glm-5.2[1m]",
					UpstreamModel: "glm-5.2",
					RequireContextSignal: &config.ModelPolicyContextSignal{
						Header:   "anthropic-beta",
						Contains: "context-1m-2025-08-07",
					},
				},
			},
			"openai": {
				{WireModel: "gpt-5.6-sol", Canonical: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol"},
			},
		},
	}
}

func decodeProjectionResponse(t *testing.T, recorder *httptest.ResponseRecorder) functionalConfigProjectionEnvelope {
	t.Helper()
	var response functionalConfigProjectionEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, recorder.Body.String())
	}
	return response
}

func projectionJSON(t *testing.T, cfg *config.Config) []byte {
	t.Helper()
	full, err := configJSONMap(canonicalizeFunctionalConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	envelope := classifyFunctionalConfig(full)
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGetFunctionalConfigExcludesSecretsAndEnvironment(t *testing.T) {
	t.Parallel()
	h := &Handler{cfg: &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL:    "http://proxy.invalid",
			APIKeys:     []string{"secret-client-key"},
			ModelPolicy: testConfiguredModelPolicy(),
		},
		TLS:       config.TLSConfig{Enable: true, Cert: "cert.pem", Key: "key.pem"},
		Plugins:   config.PluginsConfig{Enabled: true, Dir: "plugins"},
		ClaudeKey: []config.ClaudeKey{{APIKey: "secret-provider-key"}},
		Debug:     true,
	}}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config/functional", nil)
	h.GetFunctionalConfig(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeProjectionResponse(t, recorder)
	if len(response.UnclassifiedKeys) != 0 {
		t.Fatalf("unexpected unclassified keys: %v", response.UnclassifiedKeys)
	}
	if _, ok := response.Config["debug"]; !ok {
		t.Fatal("functional debug key missing")
	}
	if _, ok := response.Config["model-policy"]; !ok {
		t.Fatal("functional model-policy key missing")
	}
	for _, forbidden := range []string{"api-keys", "claude-api-key", "plugins", "proxy-url", "tls"} {
		if _, ok := response.Config[forbidden]; ok {
			t.Fatalf("forbidden key %q leaked into projection", forbidden)
		}
	}
	if strings.Contains(recorder.Body.String(), "secret-client-key") ||
		strings.Contains(recorder.Body.String(), "secret-provider-key") ||
		strings.Contains(recorder.Body.String(), "proxy.invalid") {
		t.Fatal("projection leaked a secret or environment-local value")
	}
}

func TestFunctionalConfigProjectionCanonicalizesEmptyPayloadSelectors(t *testing.T) {
	t.Parallel()
	nilSelectors := &config.Config{
		SDKConfig: config.SDKConfig{ModelPolicy: testConfiguredModelPolicy()},
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{
					Name:     "glm-5.2[1m]",
					Protocol: "anthropic",
				}},
				Params: map[string]any{"reasoning.effort": "max"},
			}},
		},
	}
	emptySelectors := nilSelectors.CloneForRuntime()
	model := &emptySelectors.Payload.Override[0].Models[0]
	model.Headers = map[string]string{}
	model.Match = []map[string]any{}
	model.NotMatch = []map[string]any{}
	model.Exist = []string{}
	model.NotExist = []string{}

	nilJSON := projectionJSON(t, nilSelectors)
	emptyJSON := projectionJSON(t, emptySelectors)
	if !bytes.Equal(nilJSON, emptyJSON) {
		t.Fatalf("semantically equivalent payload selectors differ:\nnull:  %s\nempty: %s", nilJSON, emptyJSON)
	}
}

func TestPutFunctionalConfigPreservesTargetSecretsAndEnvironment(t *testing.T) {
	t.Parallel()
	path := writeTestConfigFile(t)
	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				ProxyURL:           "http://target-proxy.invalid",
				APIKeys:            []string{"target-client-key"},
				ModelPolicy:        testConfiguredModelPolicy(),
				GPTImage2BaseModel: "gpt-target-model",
			},
			Host:      "127.0.0.1",
			Port:      8317,
			AuthDir:   "target-auths",
			TLS:       config.TLSConfig{Enable: true, Cert: "target-cert.pem", Key: "target-key.pem"},
			Plugins:   config.PluginsConfig{Enabled: true, Dir: "target-plugins"},
			ClaudeKey: []config.ClaudeKey{{APIKey: "target-provider-key"}},
			OAuthExcludedModels: map[string][]string{
				"codex": {"legacy-hidden-model"},
			},
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"codex": {{
					Name:  "legacy-upstream-model",
					Alias: "legacy-client-model",
					Fork:  true,
				}},
			},
			Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "legacy-model"}},
					Params: map[string]any{"reasoning.effort": "legacy"},
				}},
				Filter: []config.PayloadFilterRule{{
					Models: []config.PayloadModelRule{{Name: "legacy-model"}},
					Params: []string{"response_format"},
				}},
			},
			Debug:     false,
			Home: config.HomeConfig{
				Enabled: true,
				NodeID:  "target-home-node",
				Host:    "target-home.invalid",
				Port:    6380,
			},
			RemoteManagement: config.RemoteManagement{
				AllowRemote:           true,
				SecretKey:             "target-management-secret",
				DisableControlPanel:   true,
				PanelGitHubRepository: "https://target-panel.invalid/repository",
			},
		},
		configFilePath: path,
	}

	source := &config.Config{
		SDKConfig:                       config.SDKConfig{ModelPolicy: testConfiguredModelPolicy()},
		Debug:                           true,
		ErrorLogsMaxFiles:               10,
		RedisUsageQueueRetentionSeconds: 60,
		RequestRetry:                    7,
		WebsocketAuth:                   true,
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{
					Name:     "glm-5.2[1m]",
					Protocol: "anthropic",
				}},
				Params: map[string]any{"reasoning.effort": "max"},
			}},
		},
	}
	full, err := configJSONMap(canonicalizeFunctionalConfig(source))
	if err != nil {
		t.Fatal(err)
	}
	envelope := classifyFunctionalConfig(full)
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config/functional", strings.NewReader(string(body)))
	context.Request.Header.Set("Content-Type", "application/json")
	h.PutFunctionalConfig(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !h.cfg.Debug || h.cfg.RequestRetry != 7 {
		t.Fatalf("functional values not applied: debug=%v request-retry=%d", h.cfg.Debug, h.cfg.RequestRetry)
	}
	if h.cfg.GPTImage2BaseModel != "" {
		t.Fatalf("omitted functional field was not reset: gpt-image-2-base-model=%q", h.cfg.GPTImage2BaseModel)
	}
	if len(h.cfg.OAuthExcludedModels) != 0 || len(h.cfg.OAuthModelAlias) != 0 {
		t.Fatalf("omitted OAuth functional fields were not reset: excluded=%v aliases=%v", h.cfg.OAuthExcludedModels, h.cfg.OAuthModelAlias)
	}
	if len(h.cfg.Payload.Filter) != 0 {
		t.Fatalf("omitted payload filter was not reset: %#v", h.cfg.Payload.Filter)
	}
	if h.cfg.ProxyURL != "http://target-proxy.invalid" ||
		len(h.cfg.APIKeys) != 1 || h.cfg.APIKeys[0] != "target-client-key" ||
		len(h.cfg.ClaudeKey) != 1 || h.cfg.ClaudeKey[0].APIKey != "target-provider-key" ||
		h.cfg.Plugins.Dir != "target-plugins" ||
		h.cfg.TLS.Cert != "target-cert.pem" {
		t.Fatal("target secret or environment-local config was changed")
	}
	if h.cfg.Host != "127.0.0.1" ||
		h.cfg.Port != 8317 ||
		h.cfg.AuthDir != "target-auths" ||
		!h.cfg.RemoteManagement.AllowRemote ||
		h.cfg.RemoteManagement.SecretKey != "target-management-secret" ||
		h.cfg.RemoteManagement.PanelGitHubRepository != "https://target-panel.invalid/repository" ||
		h.cfg.Home.NodeID != "target-home-node" ||
		h.cfg.Home.Port != 6380 {
		t.Fatal("target server, management, auth directory, or runtime Home config was changed")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, preserved := range []string{
		"port: 8317",
		"target-management-secret",
		"target-client-key",
		"target-provider-key",
		"target-proxy.invalid",
		"target-plugins",
	} {
		if !strings.Contains(string(written), preserved) {
			t.Fatalf("persisted config did not preserve %q", preserved)
		}
	}

	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}
	if got, want := projectionJSON(t, reloaded), projectionJSON(t, source); !bytes.Equal(got, want) {
		t.Fatalf("functional projection changed after save/reload:\nwant: %s\ngot:  %s", want, got)
	}
}

func TestPutFunctionalConfigRejectsUnknownKey(t *testing.T) {
	t.Parallel()
	h := &Handler{
		cfg:            &config.Config{SDKConfig: config.SDKConfig{ModelPolicy: testConfiguredModelPolicy()}},
		configFilePath: writeTestConfigFile(t),
	}
	body := `{"schema-version":1,"config":{"model-policy":{"mode":"configured-only","catalog-allowlist":["glm-5.2[1m]"],"protocol-rules":{"anthropic":[{"wire-model":"glm-5.2[1m]","canonical":"glm-5.2[1m]","upstream-model":"glm-5.2"}]}},"future-unknown":true}}`
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config/functional", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	h.PutFunctionalConfig(context)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "future-unknown") {
		t.Fatalf("response does not identify unknown key: %s", recorder.Body.String())
	}
}
