package handlers

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelpolicy"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
)

func (h *BaseAPIHandler) admitBeforeRouter(ctx context.Context, protocol, modelName string, rawJSON []byte, execOptions modelExecutionOptions) (modelpolicy.Admission, string, []byte, *interfaces.ErrorMessage) {
	if h == nil || h.Cfg == nil || !modelpolicy.Enabled(h.Cfg.ModelPolicy) {
		return modelpolicy.Admission{}, modelName, rawJSON, nil
	}
	headers := modelExecutionHeaders(ctx, execOptions.Headers)
	admission, err := modelpolicy.Admit(h.Cfg.ModelPolicy, protocol, modelName, headers)
	if err != nil {
		return modelpolicy.Admission{}, "", nil, executionErrorMessage(err)
	}
	updated := rawJSON
	if len(rawJSON) > 0 {
		updated, err = sjson.SetBytes(rawJSON, "model", admission.Canonical)
		if err != nil {
			return modelpolicy.Admission{}, "", nil, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("rewrite admitted model: %w", err),
			}
		}
	}
	return admission, admission.Canonical, updated, nil
}

func addModelPolicyMetadata(metadata map[string]any, admission modelpolicy.Admission) {
	if metadata == nil || admission.Canonical == "" {
		return
	}
	metadata[modelpolicy.CanonicalModelMetadataKey] = admission.Canonical
	metadata[modelpolicy.ContextModeMetadataKey] = admission.ContextMode
}

func (h *BaseAPIHandler) pluginExecutionApproval(protocol, pluginID string, req coreexecutor.Request, opts *coreexecutor.Options) (modelpolicy.Approval, bool, error) {
	if h == nil || h.Cfg == nil || !modelpolicy.Enabled(h.Cfg.ModelPolicy) {
		return modelpolicy.Approval{}, false, nil
	}
	if opts == nil || opts.Metadata == nil {
		return modelpolicy.Approval{}, true, &modelpolicy.Error{Protocol: protocol, Model: req.Model, Reason: "canonical identity is missing"}
	}
	canonical, _ := opts.Metadata[modelpolicy.CanonicalModelMetadataKey].(string)
	if err := modelpolicy.ValidateFinal(h.Cfg.ModelPolicy, protocol, canonical, req.Model); err != nil {
		return modelpolicy.Approval{}, true, err
	}
	payloadModel, err := modelpolicy.ValidatePayloadModel(protocol, canonical, req.Model, req.Payload)
	if err != nil {
		return modelpolicy.Approval{}, true, err
	}
	pluginID = strings.ToLower(strings.TrimSpace(pluginID))
	identity := sha256.Sum256([]byte("plugin:" + pluginID + ":no-core-auth"))
	approval := modelpolicy.Approval{
		Protocol:         modelpolicy.NormalizeProtocol(protocol),
		Provider:         "plugin",
		AuthIdentityKind: "plugin",
		AuthIdentityHash: fmt.Sprintf("%x", identity[:]),
		ExecutorKind:     "plugin:" + pluginID,
		Canonical:        canonical,
		UpstreamModel:    req.Model,
		ContextMode:      stringMetadata(opts.Metadata, modelpolicy.ContextModeMetadataKey),
		PayloadModel:     payloadModel,
	}
	return approval, true, nil
}

func (h *BaseAPIHandler) sealPluginExecution(protocol, pluginID string, req coreexecutor.Request, opts *coreexecutor.Options) error {
	approval, enabled, err := h.pluginExecutionApproval(protocol, pluginID, req, opts)
	if err != nil || !enabled {
		return err
	}
	opts.ModelPolicyApprovalHash = approval.Hash()
	return nil
}

func (h *BaseAPIHandler) admitPluginExecution(protocol, pluginID string, req coreexecutor.Request, opts *coreexecutor.Options) error {
	approval, enabled, err := h.pluginExecutionApproval(protocol, pluginID, req, opts)
	if err != nil || !enabled {
		return err
	}
	expected := strings.TrimSpace(opts.ModelPolicyApprovalHash)
	if expected == "" {
		return &modelpolicy.Error{Protocol: protocol, Model: req.Model, Reason: "approved execution tuple is missing"}
	}
	actual := approval.Hash()
	if actual != expected {
		return &modelpolicy.Error{Protocol: protocol, Model: req.Model, Reason: "approved execution tuple changed after plugin route selection"}
	}
	opts.Metadata[modelpolicy.ApprovalHashMetadataKey] = actual
	return nil
}

func stringMetadata(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
