package config

import (
	"fmt"
	"strings"
	"unicode"
)

const configuredOnlyModelPolicyMode = "configured-only"

// ValidateModelPolicy rejects ambiguous or incomplete configured-only policies.
func ValidateModelPolicy(policy ModelPolicyConfig) error {
	mode := strings.ToLower(strings.TrimSpace(policy.Mode))
	if mode == "" {
		return nil
	}
	if mode != configuredOnlyModelPolicyMode {
		return fmt.Errorf("model-policy.mode %q is unsupported", policy.Mode)
	}
	if len(policy.CatalogAllowlist) == 0 {
		return fmt.Errorf("model-policy.catalog-allowlist must not be empty")
	}

	catalog := make(map[string]struct{}, len(policy.CatalogAllowlist))
	for index, raw := range policy.CatalogAllowlist {
		model, err := normalizePolicyModel(raw)
		if err != nil {
			return fmt.Errorf("model-policy.catalog-allowlist[%d]: %w", index, err)
		}
		if _, duplicate := catalog[model]; duplicate {
			return fmt.Errorf("model-policy.catalog-allowlist contains duplicate model %q", model)
		}
		catalog[model] = struct{}{}
	}

	reachable := make(map[string]struct{}, len(catalog))
	upstreamOwner := make(map[string]string)
	for rawProtocol, rules := range policy.ProtocolRules {
		protocol := normalizePolicyProtocol(rawProtocol)
		if protocol == "" {
			return fmt.Errorf("model-policy.protocol-rules contains unsupported protocol %q", rawProtocol)
		}
		if strings.TrimSpace(rawProtocol) != protocol {
			return fmt.Errorf("model-policy.protocol-rules key %q must use canonical spelling %q", rawProtocol, protocol)
		}
		if len(rules) == 0 {
			return fmt.Errorf("model-policy.protocol-rules.%s must not be empty", protocol)
		}
		wireModels := make(map[string]struct{}, len(rules))
		for index, rule := range rules {
			wire, err := normalizePolicyModel(rule.WireModel)
			if err != nil {
				return fmt.Errorf("model-policy.protocol-rules.%s[%d].wire-model: %w", protocol, index, err)
			}
			if _, duplicate := wireModels[wire]; duplicate {
				return fmt.Errorf("model-policy.protocol-rules.%s contains duplicate wire-model %q", protocol, wire)
			}
			wireModels[wire] = struct{}{}

			canonical, err := normalizePolicyModel(rule.Canonical)
			if err != nil {
				return fmt.Errorf("model-policy.protocol-rules.%s[%d].canonical: %w", protocol, index, err)
			}
			if _, allowed := catalog[canonical]; !allowed {
				return fmt.Errorf("model-policy.protocol-rules.%s[%d] references canonical model %q outside catalog-allowlist", protocol, index, canonical)
			}
			reachable[canonical] = struct{}{}

			upstream := canonical
			if strings.TrimSpace(rule.UpstreamModel) != "" {
				upstream, err = normalizePolicyModel(rule.UpstreamModel)
				if err != nil {
					return fmt.Errorf("model-policy.protocol-rules.%s[%d].upstream-model: %w", protocol, index, err)
				}
			}
			if owner, exists := upstreamOwner[upstream]; exists && owner != canonical {
				return fmt.Errorf("model-policy upstream model %q belongs to both %q and %q", upstream, owner, canonical)
			}
			upstreamOwner[upstream] = canonical

			if rule.ExplicitContextSuffix && !strings.HasSuffix(wire, "[1m]") {
				return fmt.Errorf("model-policy.protocol-rules.%s[%d] requires an explicit context suffix but wire-model %q has none", protocol, index, wire)
			}
			if signal := rule.RequireContextSignal; signal != nil {
				if strings.TrimSpace(signal.Header) == "" || strings.TrimSpace(signal.Contains) == "" {
					return fmt.Errorf("model-policy.protocol-rules.%s[%d].require-context-signal requires header and contains", protocol, index)
				}
			}
		}
	}
	for model := range catalog {
		if _, ok := reachable[model]; !ok {
			return fmt.Errorf("model-policy catalog model %q is not reachable from any protocol rule", model)
		}
	}
	return nil
}

func normalizePolicyProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic":
		return "anthropic"
	case "openai":
		return "openai"
	case "openai-response":
		return "openai-response"
	default:
		return ""
	}
}

func normalizePolicyModel(model string) (string, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", fmt.Errorf("model is empty")
	}
	if strings.IndexFunc(trimmed, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("model %q contains whitespace", model)
	}
	lower := strings.ToLower(trimmed)
	if strings.ContainsAny(lower, "[]") {
		if !strings.HasSuffix(lower, "[1m]") {
			return "", fmt.Errorf("model %q has an unsupported suffix", model)
		}
		base := strings.TrimSuffix(lower, "[1m]")
		if base == "" || strings.ContainsAny(base, "[]") {
			return "", fmt.Errorf("model %q is invalid", model)
		}
	}
	return lower, nil
}
