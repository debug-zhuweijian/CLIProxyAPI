package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelpolicy"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type modelPolicyExecutor struct {
	executeCalls int
	countCalls   int
	streamCalls  int
	approvalHash string
}

func (*modelPolicyExecutor) Identifier() string { return "policy-test" }

func (e *modelPolicyExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls++
	e.approvalHash, _ = opts.Metadata[modelpolicy.ApprovalHashMetadataKey].(string)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *modelPolicyExecutor) ExecuteStream(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls++
	e.approvalHash, _ = opts.Metadata[modelpolicy.ApprovalHashMetadataKey].(string)
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*modelPolicyExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *modelPolicyExecutor) CountTokens(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls++
	e.approvalHash, _ = opts.Metadata[modelpolicy.ApprovalHashMetadataKey].(string)
	return cliproxyexecutor.Response{Payload: []byte(`{"tokens":1}`)}, nil
}

func (*modelPolicyExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestApprovedExecutionWrappersEnforceFinalModelIdentity(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cfg := &internalconfig.Config{}
	cfg.ModelPolicy = internalconfig.ModelPolicyConfig{
		Mode:             modelpolicy.ModeConfiguredOnly,
		CatalogAllowlist: []string{"glm-5.2[1m]", "gpt-5.6-sol"},
		ProtocolRules: map[string][]internalconfig.ModelPolicyRule{
			"anthropic": {{
				WireModel:     "glm-5.2[1m]",
				Canonical:     "glm-5.2[1m]",
				UpstreamModel: "glm-5.2",
			}, {
				WireModel:     "glm-5.2-alt",
				Canonical:     "glm-5.2[1m]",
				UpstreamModel: "glm-5.2-alt",
			}},
		},
	}
	manager.SetConfig(cfg)

	executor := &modelPolicyExecutor{}
	auth := &Auth{ID: "auth-policy-test", Provider: "claude"}
	metadata := map[string]any{
		modelpolicy.CanonicalModelMetadataKey: "glm-5.2[1m]",
		modelpolicy.ContextModeMetadataKey:    "1m",
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("anthropic"),
		Metadata:     metadata,
	}
	allowed := cliproxyexecutor.Request{Model: "glm-5.2", Payload: []byte(`{"model":"glm-5.2","input":"before"}`)}

	if _, err := manager.executeApproved(context.Background(), executor, auth, "claude", allowed, opts); err == nil {
		t.Fatal("executeApproved accepted a request without a pre-interceptor approval seal")
	}

	sealed, err := manager.sealModelExecution(auth, "claude", executor, allowed, opts)
	if err != nil {
		t.Fatalf("sealModelExecution returned error: %v", err)
	}
	if _, err = manager.executeApproved(context.Background(), executor, auth, "claude", allowed, sealed); err != nil {
		t.Fatalf("executeApproved returned error: %v", err)
	}
	if _, err = manager.countApproved(context.Background(), executor, auth, "claude", allowed, sealed); err != nil {
		t.Fatalf("countApproved returned error: %v", err)
	}
	if _, err = manager.streamApproved(context.Background(), executor, auth, "claude", allowed, sealed); err != nil {
		t.Fatalf("streamApproved returned error: %v", err)
	}
	if executor.executeCalls != 1 || executor.countCalls != 1 || executor.streamCalls != 1 {
		t.Fatalf("allowed calls = execute:%d count:%d stream:%d", executor.executeCalls, executor.countCalls, executor.streamCalls)
	}
	if len(executor.approvalHash) != 64 {
		t.Fatalf("executor approval hash length = %d, want 64", len(executor.approvalHash))
	}

	rejected := cliproxyexecutor.Request{Model: "gpt-5.6-sol"}
	if _, err := manager.executeApproved(context.Background(), executor, auth, "claude", rejected, sealed); err == nil {
		t.Fatal("executeApproved accepted a cross-canonical upstream model")
	}
	if _, err := manager.countApproved(context.Background(), executor, auth, "claude", rejected, sealed); err == nil {
		t.Fatal("countApproved accepted a cross-canonical upstream model")
	}
	if _, err := manager.streamApproved(context.Background(), executor, auth, "claude", rejected, sealed); err == nil {
		t.Fatal("streamApproved accepted a cross-canonical upstream model")
	}
	if executor.executeCalls != 1 || executor.countCalls != 1 || executor.streamCalls != 1 {
		t.Fatalf("rejected calls reached executor: execute:%d count:%d stream:%d", executor.executeCalls, executor.countCalls, executor.streamCalls)
	}

	t.Run("provider changed after seal", func(t *testing.T) {
		if _, errChanged := manager.executeApproved(context.Background(), executor, auth, "codex", allowed, sealed); errChanged == nil {
			t.Fatal("executeApproved accepted a provider change after sealing")
		}
	})

	t.Run("allowed upstream changed after seal", func(t *testing.T) {
		changed := allowed
		changed.Model = "glm-5.2-alt"
		changed.Payload = []byte(`{"model":"glm-5.2-alt"}`)
		if _, errChanged := manager.executeApproved(context.Background(), executor, auth, "claude", changed, sealed); errChanged == nil {
			t.Fatal("executeApproved accepted another allowed upstream after sealing")
		}
	})

	t.Run("payload model changed after seal", func(t *testing.T) {
		changed := allowed
		changed.Payload = []byte(`{"model":"gpt-5.6-sol"}`)
		if _, errChanged := manager.executeApproved(context.Background(), executor, auth, "claude", changed, sealed); errChanged == nil {
			t.Fatal("executeApproved accepted a payload model change after sealing")
		}
	})

	t.Run("non-model payload change is allowed", func(t *testing.T) {
		changed := allowed
		changed.Payload = []byte(`{"model":"glm-5.2","input":"after"}`)
		if _, errChanged := manager.executeApproved(context.Background(), executor, auth, "claude", changed, sealed); errChanged != nil {
			t.Fatalf("executeApproved rejected a non-model payload change: %v", errChanged)
		}
	})
}
