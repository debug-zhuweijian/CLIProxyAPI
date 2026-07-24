package management

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const staticAuthProjectionSchemaVersion = 1

var staticAuthProjectionKeys = stringSet(
	"api-keys",
	"claude-api-key",
	"codex-api-key",
	"gemini-api-key",
	"interactions-api-key",
	"openai-compatibility",
	"plugin-store-auth",
	"vertex-api-key",
	"xai-api-key",
)

type staticAuthProjectionEnvelope struct {
	SchemaVersion int                        `json:"schema-version"`
	Config        map[string]json.RawMessage `json:"config"`
}

type staticAuthSummary struct {
	SchemaVersion int            `json:"schema-version"`
	Counts        map[string]int `json:"counts"`
}

func staticAuthProjectionFromConfig(cfg *config.Config) (staticAuthProjectionEnvelope, error) {
	full, err := configJSONMap(cfg)
	if err != nil {
		return staticAuthProjectionEnvelope{}, err
	}
	projection := make(map[string]json.RawMessage, len(staticAuthProjectionKeys))
	for key := range staticAuthProjectionKeys {
		if key == "plugin-store-auth" {
			continue
		}
		value, ok := full[key]
		if !ok {
			value = json.RawMessage("[]")
		}
		projection[key] = append(json.RawMessage(nil), value...)
	}
	pluginRaw, ok := full["plugins"]
	if !ok {
		projection["plugin-store-auth"] = json.RawMessage("[]")
		return staticAuthProjectionEnvelope{SchemaVersion: staticAuthProjectionSchemaVersion, Config: projection}, nil
	}
	var plugins map[string]json.RawMessage
	if err = json.Unmarshal(pluginRaw, &plugins); err != nil {
		return staticAuthProjectionEnvelope{}, fmt.Errorf("decode plugins config: %w", err)
	}
	storeAuth, ok := plugins["store-auth"]
	if !ok {
		storeAuth = json.RawMessage("[]")
	}
	projection["plugin-store-auth"] = append(json.RawMessage(nil), storeAuth...)
	return staticAuthProjectionEnvelope{SchemaVersion: staticAuthProjectionSchemaVersion, Config: projection}, nil
}

// GetStaticAuthConfig returns the bounded secret-bearing projection used only
// by an authenticated provider-aware sync transaction.
func (h *Handler) GetStaticAuthConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config_unavailable"})
		return
	}
	projection, err := staticAuthProjectionFromConfig(h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "projection_failed", "message": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, projection)
}

// GetStaticAuthSummary returns counts only. It never returns key material,
// identities, hashes, provider bodies or OAuth token files.
func (h *Handler) GetStaticAuthSummary(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config_unavailable"})
		return
	}
	projection, err := staticAuthProjectionFromConfig(h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "projection_failed", "message": err.Error()})
		return
	}
	counts := make(map[string]int, len(projection.Config))
	for key, raw := range projection.Config {
		var values []json.RawMessage
		if err = json.Unmarshal(raw, &values); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "projection_failed", "message": fmt.Sprintf("%s is not an array", key)})
			return
		}
		counts[key] = len(values)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, staticAuthSummary{SchemaVersion: staticAuthProjectionSchemaVersion, Counts: counts})
}

// PutStaticAuthConfig replaces only static provider/client credentials and
// plugin-store credentials. OAuth files and environment secrets are untouched.
func (h *Handler) PutStaticAuthConfig(c *gin.Context) {
	var request staticAuthProjectionEnvelope
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_projection", "message": err.Error()})
		return
	}
	if request.SchemaVersion != staticAuthProjectionSchemaVersion {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "unsupported_schema"})
		return
	}
	if len(request.Config) != len(staticAuthProjectionKeys) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":   "incomplete_projection",
			"message": "all static auth classes are required",
		})
		return
	}
	for key, raw := range request.Config {
		if _, ok := staticAuthProjectionKeys[key]; !ok {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "unclassified_key", "message": key})
			return
		}
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_projection",
				"message": fmt.Sprintf("%s must be an array", key),
			})
			return
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	topLevelUpdates := make(map[string]json.RawMessage, len(request.Config)-1)
	topLevelResetKeys := make(map[string]struct{}, len(request.Config)-1)
	for key, raw := range request.Config {
		if key == "plugin-store-auth" {
			continue
		}
		topLevelUpdates[key] = append(json.RawMessage(nil), raw...)
		topLevelResetKeys[key] = struct{}{}
	}
	merged, err := cloneConfigWithJSONProjection(h.cfg, topLevelResetKeys, topLevelUpdates)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_projection", "message": err.Error()})
		return
	}
	if err = json.Unmarshal(request.Config["plugin-store-auth"], &merged.Plugins.StoreAuth); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_projection", "message": err.Error()})
		return
	}

	previous := h.cfg
	h.cfg = merged
	if !h.persistLocked(c) {
		h.cfg = previous
	}
}
