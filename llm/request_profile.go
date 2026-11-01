package llm

import "context"

// RequestPurpose is host-owned call metadata. It is not a prompt instruction and
// does not select a different model, endpoint, deadline or retry policy.
type RequestPurpose string

const PurposeCompaction RequestPurpose = "compaction"

type requestPurposeKey struct{}

func WithRequestPurpose(ctx context.Context, purpose RequestPurpose) context.Context {
	return context.WithValue(ctx, requestPurposeKey{}, purpose)
}

func RequestPurposeFromContext(ctx context.Context) RequestPurpose {
	purpose, _ := ctx.Value(requestPurposeKey{}).(RequestPurpose)
	return purpose
}

// RequestProfile reports only host-applied defaults, never arbitrary provider
// configuration or credentials. Applied=false means the existing wire options
// were preserved; a purpose marker alone is not a performance optimisation.
type RequestProfile struct {
	Purpose         RequestPurpose `json:"purpose"`
	Applied         bool           `json:"applied"`
	Reason          string         `json:"reason"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

func (c *OpenAIClient) requestExtraBody(ctx context.Context) (map[string]any, *RequestProfile) {
	purpose := RequestPurposeFromContext(ctx)
	if purpose == "" {
		return c.extraBody, nil
	}
	profile := &RequestProfile{Purpose: purpose}
	if purpose != PurposeCompaction {
		profile.Reason = "unsupported_purpose"
		return c.extraBody, profile
	}
	if c.model != "glm-5.3" {
		profile.Reason = "model_defaults_preserved"
		return c.extraBody, profile
	}
	if _, explicit := c.extraBody["reasoning_effort"]; explicit {
		profile.Reason = "explicit_reasoning_effort_preserved"
		return c.extraBody, profile
	}
	// GLM-5.3 documents thinking enabled and effort=max as its defaults. Missing
	// thinking therefore needs no enabling switch; only supply the lighter
	// compaction effort. Preserve every explicit disabled or unfamiliar mode.
	// https://docs.bigmodel.cn/cn/guide/models/text/glm-5.3
	if value, explicit := c.extraBody["thinking"]; explicit {
		thinking, ok := value.(map[string]any)
		if !ok || thinking["type"] != "enabled" {
			profile.Reason = "explicit_thinking_preserved"
			return c.extraBody, profile
		}
	}
	extra := make(map[string]any, len(c.extraBody)+1)
	for key, value := range c.extraBody {
		extra[key] = value
	}
	extra["reasoning_effort"] = "low"
	profile.Applied, profile.Reason, profile.ReasoningEffort = true, "compaction_reasoning_default", "low"
	return extra, profile
}
