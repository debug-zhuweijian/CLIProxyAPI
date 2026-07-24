package util

import (
	"fmt"
	"strings"
)

// IsClaudeThinkingModel checks if the model is a Claude thinking model
// that requires the interleaved-thinking beta header.
func IsClaudeThinkingModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "claude") && strings.Contains(lower, "thinking")
}

const claudeDDModelPrefix = "claude-fable-5-dd-"

// EnsureClaudeModelIDPrefix rewrites model IDs for Anthropic /models listings.
// IDs that already start with "claude-" are returned unchanged; all other IDs
// become "claude-fable-5-dd-" plus the original ID with its characters reversed.
func EnsureClaudeModelIDPrefix(id string) string {
	if id == "" {
		return id
	}
	if strings.HasPrefix(id, "claude-") {
		return id
	}
	return claudeDDModelPrefix + reverseModelID(id)
}

// ResolveClaudeModelIDPrefix reverses EnsureClaudeModelIDPrefix for request routing.
// IDs that start with "claude-fable-5-dd-" are decoded by stripping the prefix and reversing
// the remainder. Optional thinking suffixes in model(value) form are preserved.
func ResolveClaudeModelIDPrefix(id string) string {
	if id == "" {
		return id
	}
	base, suffix, hasSuffix := splitModelThinkingSuffix(id)
	if !strings.HasPrefix(base, claudeDDModelPrefix) {
		return id
	}
	encoded := base[len(claudeDDModelPrefix):]
	if encoded == "" {
		return id
	}
	resolved := reverseModelID(encoded)
	if hasSuffix {
		return resolved + "(" + suffix + ")"
	}
	return resolved
}

// ResolveClaudeModelIDPrefixStrict decodes only canonical reversible fable IDs.
// Re-encoding the decoded model must reproduce the original wire ID exactly,
// ignoring only surrounding whitespace and ASCII letter case.
func ResolveClaudeModelIDPrefixStrict(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	resolved := ResolveClaudeModelIDPrefix(trimmed)
	if resolved == trimmed {
		return "", fmt.Errorf("model is not a reversible Claude fable ID")
	}
	base, suffix, hasSuffix := splitModelThinkingSuffix(resolved)
	if strings.ContainsAny(resolved, "()") && (!hasSuffix || strings.TrimSpace(suffix) == "") {
		return "", fmt.Errorf("Claude fable model ID has an invalid thinking suffix")
	}
	reencoded := EnsureClaudeModelIDPrefix(resolved)
	if hasSuffix {
		reencoded = EnsureClaudeModelIDPrefix(base) + "(" + suffix + ")"
	}
	if !strings.EqualFold(reencoded, trimmed) {
		return "", fmt.Errorf("Claude fable model ID failed round-trip validation")
	}
	return resolved, nil
}

func splitModelThinkingSuffix(model string) (base, suffix string, hasSuffix bool) {
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen == -1 || !strings.HasSuffix(model, ")") {
		return model, "", false
	}
	return model[:lastOpen], model[lastOpen+1 : len(model)-1], true
}

func reverseModelID(id string) string {
	runes := []rune(id)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
