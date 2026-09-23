package grokacp

import (
	"encoding/json"
	"strings"
)

// ModelOption is one selectable model for the hub / UI.
type ModelOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ModelState is the hub-facing snapshot of the session's model config option.
// Prefer ACP configOptions with category "model"; fall back to legacy models field.
type ModelState struct {
	ConfigID string        `json:"config_id"`
	Current  string        `json:"current"`
	Options  []ModelOption `json:"options"`
}

// Available reports whether the state has at least one selectable option.
func (m *ModelState) Available() bool {
	return m != nil && len(m.Options) > 0
}

// LabelFor returns the display name for value, or value itself.
func (m *ModelState) LabelFor(value string) string {
	if m == nil {
		return value
	}
	for _, o := range m.Options {
		if o.Value == value {
			if o.Name != "" {
				return o.Name
			}
			return o.Value
		}
	}
	return value
}

// ParseModelState extracts model selection state from session/new, session/load,
// or session/set_config_option result JSON. Prefer configOptions category "model";
// tolerate legacy models.currentModelId + models.availableModels.
func ParseModelState(raw json.RawMessage) *ModelState {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var top map[string]any
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	return ParseModelStateFromMap(top)
}

// ParseModelStateFromMap is the map form of ParseModelState.
func ParseModelStateFromMap(top map[string]any) *ModelState {
	if top == nil {
		return nil
	}
	if ms := modelStateFromConfigOptions(top["configOptions"]); ms != nil {
		return ms
	}
	// Some agents nest under "result" when raw is a full RPC envelope (tests / proxies).
	if nested, ok := top["result"].(map[string]any); ok {
		if ms := modelStateFromConfigOptions(nested["configOptions"]); ms != nil {
			return ms
		}
		if ms := modelStateFromLegacy(nested["models"]); ms != nil {
			return ms
		}
	}
	return modelStateFromLegacy(top["models"])
}

func modelStateFromConfigOptions(v any) *ModelState {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	var modelOpt map[string]any
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		cat := strings.ToLower(strAny(m["category"]))
		if cat == "model" {
			modelOpt = m
			break
		}
	}
	// Fallback: first select option whose configId looks like model.
	if modelOpt == nil {
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := strings.ToLower(strAny(m["configId"]))
			if id == "" {
				id = strings.ToLower(strAny(m["config_id"]))
			}
			typ := strings.ToLower(strAny(m["type"]))
			if (id == "model" || strings.Contains(id, "model")) && (typ == "" || typ == "select") {
				modelOpt = m
				break
			}
		}
	}
	if modelOpt == nil {
		return nil
	}
	configID := strAny(modelOpt["configId"])
	if configID == "" {
		configID = strAny(modelOpt["config_id"])
	}
	current := strAny(modelOpt["currentValue"])
	if current == "" {
		current = strAny(modelOpt["current_value"])
	}
	opts := flattenSelectOptions(modelOpt["options"])
	if len(opts) == 0 {
		return nil
	}
	return &ModelState{
		ConfigID: configID,
		Current:  current,
		Options:  opts,
	}
}

func flattenSelectOptions(v any) []ModelOption {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]ModelOption, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Grouped: { groupId, name, options: [...] }
		if nested, ok := m["options"].([]any); ok && (m["groupId"] != nil || m["group_id"] != nil || (strAny(m["value"]) == "" && nested != nil)) {
			for _, n := range nested {
				nm, ok := n.(map[string]any)
				if !ok {
					continue
				}
				if o := optionFromMap(nm); o != nil {
					out = append(out, *o)
				}
			}
			continue
		}
		if o := optionFromMap(m); o != nil {
			out = append(out, *o)
		}
	}
	return out
}

func optionFromMap(m map[string]any) *ModelOption {
	value := strAny(m["value"])
	if value == "" {
		value = strAny(m["modelId"])
	}
	if value == "" {
		value = strAny(m["model_id"])
	}
	if value == "" {
		return nil
	}
	name := strAny(m["name"])
	if name == "" {
		name = value
	}
	return &ModelOption{
		Value:       value,
		Name:        name,
		Description: strAny(m["description"]),
	}
}

func modelStateFromLegacy(v any) *ModelState {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	current := strAny(m["currentModelId"])
	if current == "" {
		current = strAny(m["current_model_id"])
	}
	if current == "" {
		current = strAny(m["current"])
	}
	var rawOpts any
	for _, key := range []string{"availableModels", "available_models", "models", "options"} {
		if m[key] != nil {
			rawOpts = m[key]
			break
		}
	}
	arr, ok := rawOpts.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	opts := make([]ModelOption, 0, len(arr))
	for _, item := range arr {
		im, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if o := optionFromMap(im); o != nil {
			opts = append(opts, *o)
		}
	}
	if len(opts) == 0 {
		return nil
	}
	return &ModelState{
		ConfigID: "", // legacy — cannot set via session/set_config_option without configId
		Current:  current,
		Options:  opts,
	}
}
