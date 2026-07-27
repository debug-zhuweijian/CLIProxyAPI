// Package modelpolicy enforces the configured public and upstream model boundary.
package modelpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode"

	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	ModeConfiguredOnly = "configured-only"

	CanonicalModelMetadataKey = "model_policy_canonical"
	ContextModeMetadataKey    = "model_policy_context_mode"
	ApprovalHashMetadataKey   = "model_policy_approval_hash"
)

// Admission is the normalized identity produced by protocol-aware admission.
type Admission struct {
	Protocol      string
	WireModel     string
	Canonical     string
	UpstreamModel string
	ContextMode   string
}

// Approval records the non-secret dimensions authorized immediately before execution.
type Approval struct {
	Protocol         string `json:"protocol"`
	Provider         string `json:"provider"`
	AuthIdentityKind string `json:"auth_identity_kind"`
	AuthIdentityHash string `json:"auth_identity_hash"`
	ExecutorKind     string `json:"executor_kind"`
	Canonical        string `json:"canonical"`
	UpstreamModel    string `json:"upstream_model"`
	ContextMode      string `json:"context_mode"`
	PayloadModel     string `json:"payload_model,omitempty"`
}

// Hash returns a deterministic digest of an approved execution tuple.
func (a Approval) Hash() string {
	payload, _ := json.Marshal(a)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Error is a request-scoped model-policy rejection.
type Error struct {
	Protocol string
	Model    string
	Reason   string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	message := fmt.Sprintf("model %q is not in configured allowlist for protocol %q", e.Model, e.Protocol)
	if strings.TrimSpace(e.Reason) != "" {
		message += ": " + e.Reason
	}
	return message
}

func (*Error) StatusCode() int       { return http.StatusBadRequest }
func (*Error) IsRequestScoped() bool { return true }

// Enabled reports whether the strict configured-only policy is active.
func Enabled(policy config.ModelPolicyConfig) bool {
	return strings.EqualFold(strings.TrimSpace(policy.Mode), ModeConfiguredOnly)
}

// NormalizeProtocol maps public protocol spellings to the internal config keys.
func NormalizeProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic", "claude":
		return "anthropic"
	case "openai", "openai-chat", "chat-completions":
		return "openai"
	case "openai-response", "openai-responses", "responses":
		return "openai-response"
	default:
		return strings.ToLower(strings.TrimSpace(protocol))
	}
}

// NormalizeModelID normalizes case and the supported explicit [1m] suffix.
func NormalizeModelID(model string) (string, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", fmt.Errorf("model is empty")
	}
	if strings.ContainsAny(trimmed, "[]") {
		lower := strings.ToLower(trimmed)
		if !strings.HasSuffix(lower, "[1m]") {
			return "", fmt.Errorf("unsupported model suffix")
		}
		base := strings.TrimRightFunc(trimmed[:len(trimmed)-len("[1m]")], unicode.IsSpace)
		if base == "" || strings.ContainsAny(base, "[]") || strings.IndexFunc(base, unicode.IsSpace) >= 0 {
			return "", fmt.Errorf("invalid model identifier")
		}
		return strings.ToLower(base) + "[1m]", nil
	}
	if strings.IndexFunc(trimmed, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("invalid model identifier")
	}
	return strings.ToLower(trimmed), nil
}

// Admit validates and canonicalizes an inbound model for a protocol.
func Admit(policy config.ModelPolicyConfig, protocol, wireModel string, headers http.Header) (Admission, error) {
	normalizedProtocol := NormalizeProtocol(protocol)
	if !Enabled(policy) {
		model, err := NormalizeModelID(wireModel)
		if err != nil {
			return Admission{}, err
		}
		return Admission{Protocol: normalizedProtocol, WireModel: model, Canonical: model, UpstreamModel: model}, nil
	}

	decodedWire, err := decodeWireModel(normalizedProtocol, wireModel)
	if err != nil {
		return Admission{}, reject(normalizedProtocol, wireModel, err.Error())
	}
	normalizedWire, err := NormalizeModelID(decodedWire)
	if err != nil {
		return Admission{}, reject(normalizedProtocol, wireModel, err.Error())
	}

	rules, ok := policy.ProtocolRules[normalizedProtocol]
	if !ok {
		return Admission{}, reject(normalizedProtocol, wireModel, "protocol has no configured rules")
	}
	for _, rule := range rules {
		ruleWire, ruleErr := NormalizeModelID(rule.WireModel)
		if ruleErr != nil || ruleWire != normalizedWire {
			continue
		}
		canonical, canonicalErr := NormalizeModelID(rule.Canonical)
		if canonicalErr != nil || !catalogContains(policy.CatalogAllowlist, canonical) {
			continue
		}
		if rule.ExplicitContextSuffix && !strings.HasSuffix(normalizedWire, "[1m]") {
			continue
		}
		contextMode := ""
		if rule.RequireContextSignal != nil {
			if !containsHeaderToken(headers, rule.RequireContextSignal.Header, rule.RequireContextSignal.Contains) {
				continue
			}
			contextMode = "1m"
		} else if strings.HasSuffix(normalizedWire, "[1m]") {
			contextMode = "1m"
		}
		upstream := rule.UpstreamModel
		if strings.TrimSpace(upstream) == "" {
			upstream = canonical
		}
		normalizedUpstream, upstreamErr := NormalizeModelID(upstream)
		if upstreamErr != nil {
			continue
		}
		return Admission{
			Protocol:      normalizedProtocol,
			WireModel:     normalizedWire,
			Canonical:     canonical,
			UpstreamModel: normalizedUpstream,
			ContextMode:   contextMode,
		}, nil
	}
	return Admission{}, reject(normalizedProtocol, wireModel, "no protocol rule matched")
}

// ValidateFinal verifies the canonical identity and actual upstream model immediately before execution.
func ValidateFinal(policy config.ModelPolicyConfig, protocol, canonical, upstreamModel string) error {
	if !Enabled(policy) {
		return nil
	}
	normalizedProtocol := NormalizeProtocol(protocol)
	normalizedCanonical, errCanonical := NormalizeModelID(canonical)
	if errCanonical != nil || !catalogContains(policy.CatalogAllowlist, normalizedCanonical) {
		return reject(normalizedProtocol, canonical, "canonical model is not allowed")
	}
	normalizedUpstream, errUpstream := NormalizeModelID(upstreamModel)
	if errUpstream != nil {
		return reject(normalizedProtocol, upstreamModel, "invalid upstream model")
	}
	for _, rule := range policy.ProtocolRules[normalizedProtocol] {
		ruleCanonical, err := NormalizeModelID(rule.Canonical)
		if err != nil || ruleCanonical != normalizedCanonical {
			continue
		}
		candidate := rule.UpstreamModel
		if strings.TrimSpace(candidate) == "" {
			candidate = rule.Canonical
		}
		normalizedCandidate, err := NormalizeModelID(candidate)
		if err == nil && normalizedCandidate == normalizedUpstream {
			return nil
		}
	}
	return reject(normalizedProtocol, upstreamModel, "upstream model does not belong to canonical model")
}

// ValidatePayloadModel verifies that a JSON request body did not replace the
// selected model identity. Empty and non-JSON bodies do not carry a model.
func ValidatePayloadModel(protocol, canonical, upstreamModel string, payload []byte) (string, error) {
	payloadModel, err := PayloadModel(payload)
	if err != nil {
		return "", reject(NormalizeProtocol(protocol), upstreamModel, err.Error())
	}
	if payloadModel == "" {
		return "", nil
	}
	normalizedCanonical, errCanonical := NormalizeModelID(canonical)
	normalizedUpstream, errUpstream := NormalizeModelID(upstreamModel)
	if errCanonical != nil || errUpstream != nil {
		return "", reject(NormalizeProtocol(protocol), payloadModel, "selected model identity is invalid")
	}
	if payloadModel != normalizedCanonical && payloadModel != normalizedUpstream {
		return "", reject(NormalizeProtocol(protocol), payloadModel, "request payload model does not match the approved canonical or upstream model")
	}
	return payloadModel, nil
}

// ValidateExecutorPayloadModel verifies that a translated executor payload
// still carries the approved upstream model immediately before execution.
func ValidateExecutorPayloadModel(protocol, upstreamModel string, payload []byte) error {
	payloadModel, err := PayloadModel(payload)
	if err != nil {
		return reject(NormalizeProtocol(protocol), upstreamModel, err.Error())
	}
	if payloadModel == "" {
		return nil
	}
	normalizedUpstream, errUpstream := NormalizeModelID(upstreamModel)
	if errUpstream != nil {
		return reject(NormalizeProtocol(protocol), upstreamModel, "approved upstream model is invalid")
	}
	if payloadModel != normalizedUpstream {
		return reject(NormalizeProtocol(protocol), payloadModel, "translated executor payload model does not match the approved upstream model")
	}
	return nil
}

// PayloadModel returns the normalized top-level JSON model field when present.
func PayloadModel(payload []byte) (string, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return "", fmt.Errorf("request payload is invalid JSON")
	}
	raw, ok := object["model"]
	if !ok {
		return "", nil
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		return "", fmt.Errorf("request payload model must be a string")
	}
	normalized, err := NormalizeModelID(model)
	if err != nil {
		return "", fmt.Errorf("request payload model is invalid: %w", err)
	}
	return normalized, nil
}

// FilterCatalog returns independent catalog entries whose normalized IDs are allowed.
func FilterCatalog(policy config.ModelPolicyConfig, models []map[string]any) []map[string]any {
	if !Enabled(policy) {
		return cloneModels(models)
	}
	out := make([]map[string]any, 0, len(models))
	seen := make(map[string]struct{})
	for _, model := range models {
		id, _ := model["id"].(string)
		decoded, err := decodeWireModel("anthropic", id)
		if err != nil {
			decoded = id
		}
		normalized, err := NormalizeModelID(decoded)
		if err != nil || !catalogContains(policy.CatalogAllowlist, normalized) {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		entry := make(map[string]any, len(model))
		for key, value := range model {
			entry[key] = value
		}
		entry["id"] = normalized
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, _ := out[i]["id"].(string)
		right, _ := out[j]["id"].(string)
		return left < right
	})
	return out
}

// CanonicalCatalog returns the configured canonical IDs as model entries.
func CanonicalCatalog(policy config.ModelPolicyConfig) []map[string]any {
	if !Enabled(policy) {
		return nil
	}
	out := make([]map[string]any, 0, len(policy.CatalogAllowlist))
	seen := make(map[string]struct{})
	for _, raw := range policy.CatalogAllowlist {
		id, err := NormalizeModelID(raw)
		if err != nil {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, map[string]any{"id": id, "object": "model", "owned_by": "configured"})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i]["id"].(string) < out[j]["id"].(string)
	})
	return out
}

// CatalogForProtocol returns the canonical models reachable through one protocol.
func CatalogForProtocol(policy config.ModelPolicyConfig, protocol string) []map[string]any {
	if !Enabled(policy) {
		return nil
	}
	protocol = NormalizeProtocol(protocol)
	allowed := make(map[string]struct{})
	for _, rule := range policy.ProtocolRules[protocol] {
		canonical, err := NormalizeModelID(rule.Canonical)
		if err == nil && catalogContains(policy.CatalogAllowlist, canonical) {
			allowed[canonical] = struct{}{}
		}
	}
	out := make([]map[string]any, 0, len(allowed))
	for canonical := range allowed {
		out = append(out, map[string]any{"id": canonical, "object": "model", "owned_by": "configured"})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i]["id"].(string) < out[j]["id"].(string)
	})
	return out
}

// CanonicalizeAvailableModel maps a discovered model to its configured canonical ID.
func CanonicalizeAvailableModel(policy config.ModelPolicyConfig, protocol, model string) (string, bool) {
	if !Enabled(policy) {
		normalized, err := NormalizeModelID(model)
		return normalized, err == nil
	}
	protocol = NormalizeProtocol(protocol)
	decoded, err := decodeWireModel(protocol, model)
	if err != nil {
		return "", false
	}
	normalized, err := NormalizeModelID(decoded)
	if err != nil {
		return "", false
	}
	for _, rule := range policy.ProtocolRules[protocol] {
		canonical, canonicalErr := NormalizeModelID(rule.Canonical)
		if canonicalErr != nil || !catalogContains(policy.CatalogAllowlist, canonical) {
			continue
		}
		wire, wireErr := NormalizeModelID(rule.WireModel)
		upstream := rule.UpstreamModel
		if strings.TrimSpace(upstream) == "" {
			upstream = rule.Canonical
		}
		normalizedUpstream, upstreamErr := NormalizeModelID(upstream)
		if normalized == canonical || wireErr == nil && normalized == wire || upstreamErr == nil && normalized == normalizedUpstream {
			return canonical, true
		}
	}
	return "", false
}

func decodeWireModel(protocol, wireModel string) (string, error) {
	trimmed := strings.TrimSpace(wireModel)
	if NormalizeProtocol(protocol) != "anthropic" || !strings.HasPrefix(strings.ToLower(trimmed), "claude-fable-5-dd-") {
		return trimmed, nil
	}
	return claudemodels.ResolveClaudeModelIDPrefixStrict(trimmed)
}

func containsHeaderToken(headers http.Header, name, token string) bool {
	name = strings.TrimSpace(name)
	token = strings.ToLower(strings.TrimSpace(token))
	if headers == nil || name == "" || token == "" {
		return false
	}
	for _, value := range headers.Values(name) {
		for _, candidate := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), token) {
				return true
			}
		}
	}
	return false
}

func catalogContains(catalog []string, model string) bool {
	for _, entry := range catalog {
		normalized, err := NormalizeModelID(entry)
		if err == nil && normalized == model {
			return true
		}
	}
	return false
}

func reject(protocol, model, reason string) error {
	return &Error{Protocol: protocol, Model: strings.TrimSpace(model), Reason: reason}
}

func cloneModels(models []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		entry := make(map[string]any, len(model))
		for key, value := range model {
			entry[key] = value
		}
		out = append(out, entry)
	}
	return out
}
