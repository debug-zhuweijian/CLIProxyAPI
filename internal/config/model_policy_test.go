package config

import "testing"

func TestValidateModelPolicy(t *testing.T) {
	valid := ModelPolicyConfig{
		Mode:             configuredOnlyModelPolicyMode,
		CatalogAllowlist: []string{"glm-5.2[1m]", "gpt-5.6-sol"},
		ProtocolRules: map[string][]ModelPolicyRule{
			"anthropic": {
				{
					WireModel:            "glm-5.2",
					Canonical:            "glm-5.2[1m]",
					UpstreamModel:        "glm-5.2",
					RequireContextSignal: &ModelPolicyContextSignal{Header: "anthropic-beta", Contains: "context-1m-2025-08-07"},
				},
				{
					WireModel:             "glm-5.2[1m]",
					Canonical:             "glm-5.2[1m]",
					UpstreamModel:         "glm-5.2",
					ExplicitContextSuffix: true,
				},
			},
			"openai": {{
				WireModel:     "gpt-5.6-sol",
				Canonical:     "gpt-5.6-sol",
				UpstreamModel: "gpt-5.6-sol",
			}},
		},
	}
	if err := ValidateModelPolicy(valid); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ModelPolicyConfig)
	}{
		{
			name: "unsupported mode",
			mutate: func(policy *ModelPolicyConfig) {
				policy.Mode = "permissive"
			},
		},
		{
			name: "duplicate catalog",
			mutate: func(policy *ModelPolicyConfig) {
				policy.CatalogAllowlist = append(policy.CatalogAllowlist, "GLM-5.2[1m]")
			},
		},
		{
			name: "unreachable catalog",
			mutate: func(policy *ModelPolicyConfig) {
				policy.CatalogAllowlist = append(policy.CatalogAllowlist, "unused")
			},
		},
		{
			name: "unsupported protocol spelling",
			mutate: func(policy *ModelPolicyConfig) {
				policy.ProtocolRules["Claude"] = policy.ProtocolRules["anthropic"]
				delete(policy.ProtocolRules, "anthropic")
			},
		},
		{
			name: "duplicate wire model",
			mutate: func(policy *ModelPolicyConfig) {
				policy.ProtocolRules["anthropic"] = append(policy.ProtocolRules["anthropic"], policy.ProtocolRules["anthropic"][1])
			},
		},
		{
			name: "canonical outside catalog",
			mutate: func(policy *ModelPolicyConfig) {
				rules := policy.ProtocolRules["openai"]
				rules[0].Canonical = "other"
				policy.ProtocolRules["openai"] = rules
			},
		},
		{
			name: "invalid context signal",
			mutate: func(policy *ModelPolicyConfig) {
				policy.ProtocolRules["anthropic"][0].RequireContextSignal.Contains = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneModelPolicyForTest(valid)
			test.mutate(&candidate)
			if err := ValidateModelPolicy(candidate); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func cloneModelPolicyForTest(source ModelPolicyConfig) ModelPolicyConfig {
	target := ModelPolicyConfig{
		Mode:             source.Mode,
		CatalogAllowlist: append([]string(nil), source.CatalogAllowlist...),
		ProtocolRules:    make(map[string][]ModelPolicyRule, len(source.ProtocolRules)),
	}
	for protocol, rules := range source.ProtocolRules {
		target.ProtocolRules[protocol] = append([]ModelPolicyRule(nil), rules...)
		for index := range target.ProtocolRules[protocol] {
			if signal := target.ProtocolRules[protocol][index].RequireContextSignal; signal != nil {
				copySignal := *signal
				target.ProtocolRules[protocol][index].RequireContextSignal = &copySignal
			}
		}
	}
	return target
}
