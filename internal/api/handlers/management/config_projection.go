package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const functionalConfigProjectionSchemaVersion = 1

var functionalConfigProjectionKeys = stringSet(
	"antigravity-signature-bypass-strict",
	"antigravity-signature-cache-enabled",
	"auth-auto-refresh-workers",
	"claude-header-defaults",
	"codex",
	"codex-header-defaults",
	"commercial-mode",
	"debug",
	"disable-claude-cloak-mode",
	"disable-cooling",
	"disable-image-generation",
	"error-logs-max-files",
	"force-model-prefix",
	"gpt-image-2-base-model",
	"logging-to-file",
	"logs-max-total-size-mb",
	"max-retry-credentials",
	"max-retry-interval",
	"model-policy",
	"nonstream-keepalive-interval",
	"oauth-excluded-models",
	"oauth-model-alias",
	"passthrough-headers",
	"payload",
	"quota-exceeded",
	"redis-usage-queue-retention-seconds",
	"request-log",
	"request-retry",
	"routing",
	"save-cooldown-status",
	"streaming",
	"transient-error-cooldown-seconds",
	"usage-statistics-enabled",
	"video-result-auth-cache-ttl",
	"ws-auth",
)

var functionalConfigSecretKeys = stringSet(
	"api-keys",
	"claude-api-key",
	"codex-api-key",
	"gemini-api-key",
	"interactions-api-key",
	"openai-compatibility",
	"vertex-api-key",
	"xai-api-key",
)

var functionalConfigEnvironmentKeys = stringSet(
	"plugins",
	"pprof",
	"proxy-url",
	"tls",
)

type functionalConfigProjectionEnvelope struct {
	SchemaVersion        int                        `json:"schema-version"`
	Config               map[string]json.RawMessage `json:"config"`
	EnvironmentLocalKeys []string                   `json:"environment-local-keys,omitempty"`
	SecretKeys           []string                   `json:"secret-keys,omitempty"`
	UnclassifiedKeys     []string                   `json:"unclassified-keys"`
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func sortedSetKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func configJSONMap(cfg *config.Config) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	result := make(map[string]json.RawMessage)
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode config map: %w", err)
	}
	return result, nil
}

func canonicalizeFunctionalConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	canonical := cfg.CloneForRuntime()
	if len(canonical.Payload.Default) == 0 {
		canonical.Payload.Default = nil
	}
	if len(canonical.Payload.DefaultRaw) == 0 {
		canonical.Payload.DefaultRaw = nil
	}
	if len(canonical.Payload.Override) == 0 {
		canonical.Payload.Override = nil
	}
	if len(canonical.Payload.OverrideRaw) == 0 {
		canonical.Payload.OverrideRaw = nil
	}
	if len(canonical.Payload.Filter) == 0 {
		canonical.Payload.Filter = nil
	}
	canonicalizePayloadRuleModels(canonical.Payload.Default)
	canonicalizePayloadRuleModels(canonical.Payload.DefaultRaw)
	canonicalizePayloadRuleModels(canonical.Payload.Override)
	canonicalizePayloadRuleModels(canonical.Payload.OverrideRaw)
	for ruleIndex := range canonical.Payload.Filter {
		canonicalizePayloadModelRules(canonical.Payload.Filter[ruleIndex].Models)
	}
	return canonical
}

func canonicalizePayloadRuleModels(rules []config.PayloadRule) {
	for ruleIndex := range rules {
		canonicalizePayloadModelRules(rules[ruleIndex].Models)
	}
}

func canonicalizePayloadModelRules(models []config.PayloadModelRule) {
	for modelIndex := range models {
		model := &models[modelIndex]
		if len(model.Headers) == 0 {
			model.Headers = nil
		}
		if len(model.Match) == 0 {
			model.Match = nil
		}
		if len(model.NotMatch) == 0 {
			model.NotMatch = nil
		}
		if len(model.Exist) == 0 {
			model.Exist = nil
		}
		if len(model.NotExist) == 0 {
			model.NotExist = nil
		}
	}
}

func cloneConfigWithJSONProjection(
	cfg *config.Config,
	resetKeys map[string]struct{},
	updates map[string]json.RawMessage,
) (*config.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	merged := cfg.CloneForRuntime()
	resetJSONTaggedFields(reflect.ValueOf(merged).Elem(), resetKeys)

	data, err := json.Marshal(updates)
	if err != nil {
		return nil, fmt.Errorf("marshal projection: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(merged); err != nil {
		return nil, fmt.Errorf("decode projection: %w", err)
	}
	return merged, nil
}

func resetJSONTaggedFields(value reflect.Value, resetKeys map[string]struct{}) {
	if !value.IsValid() {
		return
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		resetJSONTaggedFields(value.Elem(), resetKeys)
		return
	}
	if value.Kind() != reflect.Struct {
		return
	}

	valueType := value.Type()
	for index := 0; index < value.NumField(); index++ {
		fieldType := valueType.Field(index)
		fieldValue := value.Field(index)
		tag := fieldType.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if fieldType.Anonymous && name == "" {
			resetJSONTaggedFields(fieldValue, resetKeys)
			continue
		}
		if name == "" {
			name = fieldType.Name
		}
		if name == "-" {
			continue
		}
		if _, ok := resetKeys[name]; ok && fieldValue.CanSet() {
			fieldValue.SetZero()
		}
	}
}

func classifyFunctionalConfig(full map[string]json.RawMessage) functionalConfigProjectionEnvelope {
	projection := make(map[string]json.RawMessage)
	unclassified := make([]string, 0)
	for key, value := range full {
		if _, ok := functionalConfigProjectionKeys[key]; ok {
			projection[key] = append(json.RawMessage(nil), value...)
			continue
		}
		if _, ok := functionalConfigSecretKeys[key]; ok {
			continue
		}
		if _, ok := functionalConfigEnvironmentKeys[key]; ok {
			continue
		}
		unclassified = append(unclassified, key)
	}
	sort.Strings(unclassified)
	return functionalConfigProjectionEnvelope{
		SchemaVersion:        functionalConfigProjectionSchemaVersion,
		Config:               projection,
		EnvironmentLocalKeys: sortedSetKeys(functionalConfigEnvironmentKeys),
		SecretKeys:           sortedSetKeys(functionalConfigSecretKeys),
		UnclassifiedKeys:     unclassified,
	}
}

// GetFunctionalConfig returns the non-secret, environment-independent config
// projection used by local/VPS consistency tooling.
func (h *Handler) GetFunctionalConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config_unavailable"})
		return
	}
	full, err := configJSONMap(canonicalizeFunctionalConfig(h.cfg))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "projection_failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, classifyFunctionalConfig(full))
}

// PutFunctionalConfig atomically replaces only the functional projection.
// Secrets, provider credentials, plugin state, proxy and TLS settings remain
// owned by the target environment.
func (h *Handler) PutFunctionalConfig(c *gin.Context) {
	var request functionalConfigProjectionEnvelope
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_projection", "message": err.Error()})
		return
	}
	if request.SchemaVersion != functionalConfigProjectionSchemaVersion {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":   "unsupported_schema",
			"message": fmt.Sprintf("schema-version must be %d", functionalConfigProjectionSchemaVersion),
		})
		return
	}
	if request.Config == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_projection", "message": "config is required"})
		return
	}
	if _, ok := request.Config["model-policy"]; !ok {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_projection", "message": "model-policy is required"})
		return
	}
	for key := range request.Config {
		if _, ok := functionalConfigProjectionKeys[key]; !ok {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "unclassified_key",
				"message": fmt.Sprintf("functional config key %q is not classified", key),
			})
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	current, err := configJSONMap(h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "projection_failed", "message": err.Error()})
		return
	}
	classified := classifyFunctionalConfig(current)
	if len(classified.UnclassifiedKeys) > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":             "unclassified_target_config",
			"unclassified-keys": classified.UnclassifiedKeys,
		})
		return
	}

	merged, err := cloneConfigWithJSONProjection(h.cfg, functionalConfigProjectionKeys, request.Config)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_projection", "message": err.Error()})
		return
	}
	if err = config.ValidateModelPolicy(merged.ModelPolicy); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_model_policy", "message": err.Error()})
		return
	}

	previous := h.cfg
	h.cfg = merged
	if !h.persistLocked(c) {
		h.cfg = previous
	}
}
