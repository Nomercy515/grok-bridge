package grokacp

import (
	"encoding/json"
	"testing"
)

func TestModelConfigFromOptionsFlat(t *testing.T) {
	raw := json.RawMessage(`{
		"configOptions": [
			{"configId":"mode","category":"mode","type":"select","currentValue":"ask","options":[{"value":"ask","name":"Ask"}]},
			{"configId":"model","name":"Model","category":"model","type":"select","currentValue":"grok-4",
			 "options":[{"value":"grok-4","name":"Grok 4"},{"value":"grok-4-fast","name":"Grok 4 Fast"}]}
		]
	}`)
	opts := parseConfigOptions(raw)
	mc := ModelConfigFromOptions(opts)
	if !mc.Available || mc.ConfigID != "model" || mc.CurrentValue != "grok-4" {
		t.Fatalf("unexpected: %+v", mc)
	}
	if len(mc.Models) != 2 || mc.Models[0].ID != "grok-4" {
		t.Fatalf("models: %+v", mc.Models)
	}
}

func TestModelConfigFromOptionsGrouped(t *testing.T) {
	raw := json.RawMessage(` [{
		"configId":"model","category":"model","type":"select","currentValue":"m1",
		"options":[{"groupId":"rec","name":"Recommended","options":[{"value":"m1","name":"M1"}]}]
	}]`)
	mc := ModelConfigFromOptions(parseConfigOptions(raw))
	if !mc.Available || len(mc.Models) != 1 || mc.Models[0].Group != "Recommended" {
		t.Fatalf("unexpected: %+v", mc)
	}
}
