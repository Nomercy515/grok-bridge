package grokacp

import (
	"encoding/json"
	"strings"
)

// ConfigOption mirrors ACP session configOptions entries.
type ConfigOption struct {
	ConfigID     string          `json:"configId"`
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	Category     string          `json:"category,omitempty"`
	Type         string          `json:"type,omitempty"`
	CurrentValue any             `json:"currentValue,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"` // flat values or groups
}

// ModelChoice is a flattened select value for the Bridge UI.
type ModelChoice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
}

// ModelConfig is the Bridge-facing model selector payload.
type ModelConfig struct {
	ConfigID     string        `json:"configId"`
	Name         string        `json:"name,omitempty"`
	CurrentValue string        `json:"currentValue"`
	Models       []ModelChoice `json:"models"`
	Available    bool          `json:"available"`
	Reason       string        `json:"reason,omitempty"`
}

func parseConfigOptions(raw json.RawMessage) []ConfigOption {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var wrap struct {
		ConfigOptions []ConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(raw, &wrap) == nil && len(wrap.ConfigOptions) > 0 {
		return wrap.ConfigOptions
	}
	var list []ConfigOption
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	return nil
}

func findModelOption(opts []ConfigOption) *ConfigOption {
	var firstSelect *ConfigOption
	for i := range opts {
		o := &opts[i]
		cat := strings.ToLower(strings.TrimSpace(o.Category))
		if cat == "model" {
			return o
		}
		if firstSelect == nil && strings.EqualFold(o.Type, "select") &&
			(strings.EqualFold(o.ConfigID, "model") || strings.EqualFold(o.Name, "model")) {
			firstSelect = o
		}
	}
	return firstSelect
}

func flattenModelChoices(opt *ConfigOption) []ModelChoice {
	if opt == nil || len(opt.Options) == 0 {
		return nil
	}
	var flat []struct {
		Value       string `json:"value"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if json.Unmarshal(opt.Options, &flat) == nil && len(flat) > 0 && flat[0].Value != "" {
		out := make([]ModelChoice, 0, len(flat))
		for _, v := range flat {
			if v.Value == "" {
				continue
			}
			name := v.Name
			if name == "" {
				name = v.Value
			}
			out = append(out, ModelChoice{ID: v.Value, Name: name, Description: v.Description})
		}
		return out
	}
	var groups []struct {
		GroupID string `json:"groupId"`
		Name    string `json:"name"`
		Options []struct {
			Value       string `json:"value"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if json.Unmarshal(opt.Options, &groups) == nil {
		out := make([]ModelChoice, 0)
		for _, g := range groups {
			gname := g.Name
			if gname == "" {
				gname = g.GroupID
			}
			for _, v := range g.Options {
				if v.Value == "" {
					continue
				}
				name := v.Name
				if name == "" {
					name = v.Value
				}
				out = append(out, ModelChoice{
					ID: v.Value, Name: name, Description: v.Description, Group: gname,
				})
			}
		}
		return out
	}
	return nil
}

// ModelConfigFromOptions extracts the model category select for the UI.
func ModelConfigFromOptions(opts []ConfigOption) ModelConfig {
	opt := findModelOption(opts)
	if opt == nil {
		return ModelConfig{Available: false, Reason: "no model configOption from ACP"}
	}
	cur, _ := opt.CurrentValue.(string)
	if cur == "" {
		cur = strAny(opt.CurrentValue)
	}
	models := flattenModelChoices(opt)
	return ModelConfig{
		ConfigID:     opt.ConfigID,
		Name:         opt.Name,
		CurrentValue: cur,
		Models:       models,
		Available:    len(models) > 0,
		Reason: func() string {
			if len(models) == 0 {
				return "model configOption has no selectable values"
			}
			return ""
		}(),
	}
}
