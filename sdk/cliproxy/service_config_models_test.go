package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigModelsPreservesCaseDistinctAliases(t *testing.T) {
	models := buildConfigModels([]config.ClaudeModel{
		{Name: "glm-5.2", Alias: "glm-5.2"},
		{Name: "GLM-5.2", Alias: "GLM-5.2"},
		{Name: "glm-5.2", Alias: "glm-5.2[1m]"},
		{Name: "GLM-5.2", Alias: "GLM-5.2[1m]"},
	}, "anthropic", "claude")

	got := make([]string, 0, len(models))
	for _, model := range models {
		got = append(got, model.ID)
	}

	want := []string{"glm-5.2", "GLM-5.2", "glm-5.2[1m]", "GLM-5.2[1m]"}
	if len(got) != len(want) {
		t.Fatalf("expected %d models, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("model[%d] = %q, want %q; all=%#v", i, got[i], want[i], got)
		}
	}
}

func TestBuildConfigModelsDeduplicatesExactAlias(t *testing.T) {
	models := buildConfigModels([]config.ClaudeModel{
		{Name: "glm-5.2", Alias: "GLM-5.2"},
		{Name: "GLM-5.2", Alias: "GLM-5.2"},
	}, "anthropic", "claude")

	if len(models) != 1 {
		t.Fatalf("expected exact duplicate alias to be collapsed, got %d", len(models))
	}
	if models[0].ID != "GLM-5.2" {
		t.Fatalf("model ID = %q, want GLM-5.2", models[0].ID)
	}
}
