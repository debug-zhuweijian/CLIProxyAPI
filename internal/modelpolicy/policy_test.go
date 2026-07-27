package modelpolicy

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestNormalizeModelID(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{" GLM-5.2[1M] ", "glm-5.2[1m]", true},
		{"glm-5.2 [1m]", "glm-5.2[1m]", true},
		{"gpt-5.6-sol", "gpt-5.6-sol", true},
		{"glm -5.2", "", false},
		{"glm-5.2[ 1m ]", "", false},
		{"glm-5.2[1m][1m]", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, err := NormalizeModelID(tt.input)
		if (err == nil) != tt.ok || got != tt.want {
			t.Fatalf("NormalizeModelID(%q) = %q, %v; want %q, ok=%v", tt.input, got, err, tt.want, tt.ok)
		}
	}
}

func TestAdmitProtocolRules(t *testing.T) {
	policy := dualModelPolicy()
	headers := http.Header{"Anthropic-Beta": []string{"feature, context-1m-2025-08-07"}}
	fakeHeaders := http.Header{"Anthropic-Beta": []string{"feature, not-context-1m-2025-08-07-extra"}}

	tests := []struct {
		name      string
		protocol  string
		model     string
		headers   http.Header
		canonical string
		upstream  string
		ok        bool
	}{
		{"anthropic plain glm with proof", "claude", "glm-5.2", headers, "glm-5.2[1m]", "glm-5.2", true},
		{"anthropic plain glm without proof", "claude", "glm-5.2", nil, "", "", false},
		{"anthropic plain glm with substring only", "claude", "glm-5.2", fakeHeaders, "", "", false},
		{"anthropic explicit glm", "anthropic", "GLM-5.2 [1M]", nil, "glm-5.2[1m]", "glm-5.2", true},
		{"openai explicit glm", "openai", "glm-5.2[1m]", nil, "glm-5.2[1m]", "glm-5.2", true},
		{"openai plain glm rejected", "openai", "glm-5.2", headers, "", "", false},
		{"responses gpt", "openai-response", "gpt-5.6-sol", nil, "gpt-5.6-sol", "gpt-5.6-sol", true},
		{"responses glm rejected", "openai-response", "glm-5.2[1m]", nil, "", "", false},
		{"third model rejected", "openai", "gpt-5.5", nil, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Admit(policy, tt.protocol, tt.model, tt.headers)
			if (err == nil) != tt.ok {
				t.Fatalf("Admit() error = %v, want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				if err == nil || !strings.Contains(err.Error(), "not in configured allowlist") {
					t.Fatalf("rejection = %v, want allowlist error", err)
				}
				return
			}
			if got.Canonical != tt.canonical || got.UpstreamModel != tt.upstream {
				t.Fatalf("Admission = %#v, want canonical=%q upstream=%q", got, tt.canonical, tt.upstream)
			}
		})
	}
}

func TestAdmitStrictClaudeFableRoundTrip(t *testing.T) {
	policy := dualModelPolicy()
	wire := claudemodels.EnsureClaudeModelIDPrefix("glm-5.2[1m]")
	got, err := Admit(policy, "anthropic", wire, nil)
	if err != nil {
		t.Fatalf("Admit(valid fable) error = %v", err)
	}
	if got.Canonical != "glm-5.2[1m]" {
		t.Fatalf("canonical = %q", got.Canonical)
	}

	if _, err = Admit(policy, "anthropic", "claude-fable-5-dd-", nil); err == nil {
		t.Fatal("Admit(invalid fable) succeeded")
	}
}

func TestValidateFinalPreventsCrossCanonicalFallback(t *testing.T) {
	policy := dualModelPolicy()
	if err := ValidateFinal(policy, "openai", "glm-5.2[1m]", "glm-5.2"); err != nil {
		t.Fatalf("ValidateFinal(GLM) error = %v", err)
	}
	if err := ValidateFinal(policy, "openai", "gpt-5.6-sol", "gpt-5.6-sol"); err != nil {
		t.Fatalf("ValidateFinal(GPT) error = %v", err)
	}
	if err := ValidateFinal(policy, "openai", "gpt-5.6-sol", "glm-5.2"); err == nil {
		t.Fatal("ValidateFinal(cross canonical) succeeded")
	}
	if err := ValidateFinal(policy, "openai-response", "glm-5.2[1m]", "glm-5.2"); err == nil {
		t.Fatal("ValidateFinal(cross protocol) succeeded")
	}
}

func TestCanonicalCatalogIsExactAndStable(t *testing.T) {
	got := CanonicalCatalog(dualModelPolicy())
	ids := make([]string, 0, len(got))
	for _, model := range got {
		ids = append(ids, model["id"].(string))
	}
	want := []string{"glm-5.2[1m]", "gpt-5.6-sol"}
	if !reflect.DeepEqual(gotIDs(ids), want) {
		t.Fatalf("catalog IDs = %#v, want %#v", ids, want)
	}
}

func TestCanonicalizeAvailableModel(t *testing.T) {
	policy := dualModelPolicy()
	tests := []struct {
		protocol string
		model    string
		want     string
		ok       bool
	}{
		{protocol: "anthropic", model: "glm-5.2", want: "glm-5.2[1m]", ok: true},
		{protocol: "anthropic", model: "glm-5.2[1m]", want: "glm-5.2[1m]", ok: true},
		{protocol: "openai-response", model: "gpt-5.6-sol", want: "gpt-5.6-sol", ok: true},
		{protocol: "openai", model: "third-model", ok: false},
		{protocol: "gemini", model: "gpt-5.6-sol", ok: false},
	}
	for _, test := range tests {
		got, ok := CanonicalizeAvailableModel(policy, test.protocol, test.model)
		if ok != test.ok || got != test.want {
			t.Fatalf("CanonicalizeAvailableModel(%q, %q) = (%q, %v), want (%q, %v)", test.protocol, test.model, got, ok, test.want, test.ok)
		}
	}
}

func TestValidatePayloadModel(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		want      string
		wantError bool
	}{
		{name: "canonical", payload: `{"model":"glm-5.2[1m]","input":"x"}`, want: "glm-5.2[1m]"},
		{name: "upstream", payload: `{"model":"glm-5.2","input":"x"}`, want: "glm-5.2"},
		{name: "no model", payload: `{"input":"x"}`},
		{name: "non json", payload: "multipart-body"},
		{name: "other allowed canonical", payload: `{"model":"gpt-5.6-sol"}`, wantError: true},
		{name: "third model", payload: `{"model":"third-model"}`, wantError: true},
		{name: "non string", payload: `{"model":42}`, wantError: true},
		{name: "invalid json", payload: `{"model":`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ValidatePayloadModel("openai", "glm-5.2[1m]", "glm-5.2", []byte(test.payload))
			if test.wantError {
				if err == nil {
					t.Fatalf("ValidatePayloadModel(%q) succeeded", test.payload)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ValidatePayloadModel(%q) = (%q, %v), want (%q, nil)", test.payload, got, err, test.want)
			}
		})
	}
}

func TestValidateExecutorPayloadModel(t *testing.T) {
	if err := ValidateExecutorPayloadModel("anthropic", "glm-5.2", []byte(`{"model":"glm-5.2"}`)); err != nil {
		t.Fatalf("ValidateExecutorPayloadModel(approved) error = %v", err)
	}
	if err := ValidateExecutorPayloadModel("anthropic", "glm-5.2", []byte(`{"messages":[]}`)); err != nil {
		t.Fatalf("ValidateExecutorPayloadModel(no model) error = %v", err)
	}
	err := ValidateExecutorPayloadModel("anthropic", "glm-5.2", []byte(`{"model":"gpt-5.6-sol"}`))
	if err == nil || !strings.Contains(err.Error(), "translated executor payload model") {
		t.Fatalf("ValidateExecutorPayloadModel(mutated) error = %v, want translated model rejection", err)
	}
}

func gotIDs(ids []string) []string { return ids }

func dualModelPolicy() config.ModelPolicyConfig {
	return config.ModelPolicyConfig{
		Mode:             ModeConfiguredOnly,
		CatalogAllowlist: []string{"glm-5.2[1m]", "gpt-5.6-sol"},
		ProtocolRules: map[string][]config.ModelPolicyRule{
			"anthropic": {
				{
					WireModel:     "glm-5.2",
					Canonical:     "glm-5.2[1m]",
					UpstreamModel: "glm-5.2",
					RequireContextSignal: &config.ModelPolicyContextSignal{
						Header: "anthropic-beta", Contains: "context-1m-2025-08-07",
					},
				},
				{WireModel: "glm-5.2[1m]", Canonical: "glm-5.2[1m]", UpstreamModel: "glm-5.2", ExplicitContextSuffix: true},
				{WireModel: "gpt-5.6-sol", Canonical: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol"},
			},
			"openai": {
				{WireModel: "glm-5.2[1m]", Canonical: "glm-5.2[1m]", UpstreamModel: "glm-5.2", ExplicitContextSuffix: true},
				{WireModel: "gpt-5.6-sol", Canonical: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol"},
			},
			"openai-response": {
				{WireModel: "gpt-5.6-sol", Canonical: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol"},
			},
		},
	}
}
