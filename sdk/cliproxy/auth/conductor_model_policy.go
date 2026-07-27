package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelpolicy"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func (m *Manager) modelExecutionApproval(auth *Auth, provider string, executor ProviderExecutor, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (modelpolicy.Approval, bool, error) {
	if m == nil {
		return modelpolicy.Approval{}, false, nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || !modelpolicy.Enabled(cfg.ModelPolicy) {
		return modelpolicy.Approval{}, false, nil
	}
	canonical := metadataString(opts.Metadata, modelpolicy.CanonicalModelMetadataKey)
	protocol := opts.SourceFormat.String()
	if err := modelpolicy.ValidateFinal(cfg.ModelPolicy, protocol, canonical, req.Model); err != nil {
		return modelpolicy.Approval{}, true, err
	}
	payloadModel, err := modelpolicy.ValidatePayloadModel(protocol, canonical, req.Model, req.Payload)
	if err != nil {
		return modelpolicy.Approval{}, true, err
	}
	kind, identityHash := modelPolicyAuthIdentity(auth)
	executorKind := ""
	if executor != nil {
		executorKind = strings.TrimSpace(executor.Identifier())
	}
	approval := modelpolicy.Approval{
		Protocol:         modelpolicy.NormalizeProtocol(protocol),
		Provider:         strings.ToLower(strings.TrimSpace(provider)),
		AuthIdentityKind: kind,
		AuthIdentityHash: identityHash,
		ExecutorKind:     executorKind,
		Canonical:        canonical,
		UpstreamModel:    req.Model,
		ContextMode:      metadataString(opts.Metadata, modelpolicy.ContextModeMetadataKey),
		PayloadModel:     payloadModel,
	}
	return approval, true, nil
}

func (m *Manager) sealModelExecution(auth *Auth, provider string, executor ProviderExecutor, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Options, error) {
	approval, enabled, err := m.modelExecutionApproval(auth, provider, executor, req, opts)
	if err != nil || !enabled {
		return opts, err
	}
	opts.ModelPolicyApprovalHash = approval.Hash()
	return opts, nil
}

func (m *Manager) approveModelExecution(auth *Auth, provider string, executor ProviderExecutor, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Options, error) {
	approval, enabled, err := m.modelExecutionApproval(auth, provider, executor, req, opts)
	if err != nil || !enabled {
		return opts, err
	}
	expected := strings.TrimSpace(opts.ModelPolicyApprovalHash)
	if expected == "" {
		return opts, &modelpolicy.Error{Protocol: opts.SourceFormat.String(), Model: req.Model, Reason: "approved execution tuple is missing"}
	}
	actual := approval.Hash()
	if actual != expected {
		return opts, &modelpolicy.Error{Protocol: opts.SourceFormat.String(), Model: req.Model, Reason: "approved execution tuple changed after credential selection"}
	}
	opts.Metadata = cloneSchedulerAnyMap(opts.Metadata)
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any)
	}
	opts.Metadata[modelpolicy.ApprovalHashMetadataKey] = actual
	return opts, nil
}

func (m *Manager) executeApproved(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	approved, err := m.approveModelExecution(auth, provider, executor, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return executor.Execute(ctx, auth, req, approved)
}

func (m *Manager) countApproved(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	approved, err := m.approveModelExecution(auth, provider, executor, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return executor.CountTokens(ctx, auth, req, approved)
}

func (m *Manager) streamApproved(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	approved, err := m.approveModelExecution(auth, provider, executor, req, opts)
	if err != nil {
		return nil, err
	}
	return executor.ExecuteStream(ctx, auth, req, approved)
}

func modelPolicyAuthIdentity(auth *Auth) (string, string) {
	kind := "missing"
	value := ""
	if auth != nil {
		switch {
		case strings.TrimSpace(auth.ID) != "":
			kind, value = "auth-id", strings.TrimSpace(auth.ID)
		case strings.TrimSpace(auth.Index) != "":
			kind, value = "auth-index", strings.TrimSpace(auth.Index)
		case strings.TrimSpace(auth.FileName) != "":
			kind, value = "auth-file", strings.TrimSpace(auth.FileName)
		default:
			kind, value = "provider", strings.TrimSpace(auth.Provider)
		}
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return kind, fmt.Sprintf("%x", sum[:])
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
