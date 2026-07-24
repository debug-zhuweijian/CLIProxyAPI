package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func TestGetStaticAuthSummaryReturnsCountsWithoutSecrets(t *testing.T) {
	t.Parallel()
	h := &Handler{cfg: &config.Config{
		SDKConfig: config.SDKConfig{APIKeys: []string{"client-secret"}},
		ClaudeKey: []config.ClaudeKey{{APIKey: "provider-secret"}},
	}}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config/static-auth/summary", nil)
	h.GetStaticAuthSummary(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "client-secret") || strings.Contains(recorder.Body.String(), "provider-secret") {
		t.Fatal("static auth summary leaked credential material")
	}
	var summary staticAuthSummary
	if err := json.Unmarshal(recorder.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Counts["api-keys"] != 1 || summary.Counts["claude-api-key"] != 1 {
		t.Fatalf("unexpected counts: %#v", summary.Counts)
	}
}

func TestPutStaticAuthConfigPreservesFunctionalAndEnvironmentConfig(t *testing.T) {
	t.Parallel()
	var targetPlugins config.PluginsConfig
	if err := yaml.Unmarshal([]byte(`
enabled: true
dir: target-plugins
configs:
  sample:
    enabled: true
    custom-setting: preserve-me
`), &targetPlugins); err != nil {
		t.Fatal(err)
	}
	target := &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL:    "http://target-proxy.invalid",
			APIKeys:     []string{"old-client"},
			ModelPolicy: testConfiguredModelPolicy(),
		},
		Host:    "127.0.0.1",
		Port:    8317,
		AuthDir: "target-auths",
		Debug:   true,
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
		Plugins:   targetPlugins,
		ClaudeKey: []config.ClaudeKey{{APIKey: "old-provider"}},
	}
	source := &config.Config{
		SDKConfig: config.SDKConfig{APIKeys: []string{"new-client"}},
		ClaudeKey: []config.ClaudeKey{{APIKey: "new-provider"}},
	}
	projection, err := staticAuthProjectionFromConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: target, configFilePath: writeTestConfigFile(t)}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config/static-auth", strings.NewReader(string(body)))
	context.Request.Header.Set("Content-Type", "application/json")
	h.PutStaticAuthConfig(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(h.cfg.APIKeys) != 1 || h.cfg.APIKeys[0] != "new-client" ||
		len(h.cfg.ClaudeKey) != 1 || h.cfg.ClaudeKey[0].APIKey != "new-provider" {
		t.Fatal("static credentials were not replaced")
	}
	if h.cfg.ProxyURL != "http://target-proxy.invalid" ||
		!h.cfg.Debug ||
		h.cfg.Plugins.Dir != "target-plugins" ||
		h.cfg.ModelPolicy.Mode != "configured-only" {
		t.Fatal("functional or environment-local config changed")
	}
	if h.cfg.Host != "127.0.0.1" ||
		h.cfg.Port != 8317 ||
		h.cfg.AuthDir != "target-auths" ||
		!h.cfg.RemoteManagement.AllowRemote ||
		h.cfg.RemoteManagement.SecretKey != "target-management-secret" ||
		h.cfg.Home.NodeID != "target-home-node" ||
		h.cfg.Home.Port != 6380 {
		t.Fatal("target server, management, auth directory, or runtime Home config changed")
	}
	pluginYAML, err := yaml.Marshal(h.cfg.Plugins.Configs["sample"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pluginYAML), "custom-setting: preserve-me") {
		t.Fatalf("plugin custom config was lost: %s", string(pluginYAML))
	}
}

func TestPutStaticAuthConfigRejectsPartialProjection(t *testing.T) {
	t.Parallel()
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/v0/management/config/static-auth",
		strings.NewReader(`{"schema-version":1,"config":{"api-keys":[]}}`),
	)
	context.Request.Header.Set("Content-Type", "application/json")
	h.PutStaticAuthConfig(context)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
}
