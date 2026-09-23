package grokacp

import (
	"encoding/json"
	"testing"
)

func TestParseModelStateConfigOptions(t *testing.T) {
	raw := json.RawMessage(`{
		"sessionId": "s1",
		"configOptions": [
			{
				"configId": "mode",
				"category": "mode",
				"type": "select",
				"currentValue": "ask",
				"options": [{"value": "ask", "name": "Ask"}]
			},
			{
				"configId": "model",
				"name": "Model",
				"category": "model",
				"type": "select",
				"currentValue": "model-1",
				"options": [
					{"value": "model-1", "name": "Model 1", "description": "Fast"},
					{"value": "model-2", "name": "Model 2"}
				]
			}
		]
	}`)
	ms := ParseModelState(raw)
	if ms == nil || !ms.Available() {
		t.Fatalf("nil/unavailable: %+v", ms)
	}
	if ms.ConfigID != "model" || ms.Current != "model-1" {
		t.Fatalf("got %+v", ms)
	}
	if len(ms.Options) != 2 || ms.Options[0].Name != "Model 1" {
		t.Fatalf("options=%+v", ms.Options)
	}
	if ms.LabelFor("model-2") != "Model 2" {
		t.Fatalf("label=%q", ms.LabelFor("model-2"))
	}
}

func TestParseModelStateGroupedOptions(t *testing.T) {
	raw := json.RawMessage(`{
		"configOptions": [{
			"configId": "model",
			"category": "model",
			"type": "select",
			"currentValue": "a",
			"options": [{
				"groupId": "rec",
				"name": "Recommended",
				"options": [
					{"value": "a", "name": "A"},
					{"value": "b", "name": "B"}
				]
			}]
		}]
	}`)
	ms := ParseModelState(raw)
	if ms == nil || len(ms.Options) != 2 {
		t.Fatalf("got %+v", ms)
	}
}

func TestParseModelStateLegacy(t *testing.T) {
	raw := json.RawMessage(`{
		"sessionId": "s1",
		"models": {
			"currentModelId": "legacy-1",
			"availableModels": [
				{"modelId": "legacy-1", "name": "Legacy One"},
				{"modelId": "legacy-2", "name": "Legacy Two"}
			]
		}
	}`)
	ms := ParseModelState(raw)
	if ms == nil || ms.Current != "legacy-1" || ms.ConfigID != "" {
		t.Fatalf("got %+v", ms)
	}
	if len(ms.Options) != 2 || ms.Options[0].Value != "legacy-1" {
		t.Fatalf("options=%+v", ms.Options)
	}
}

func TestParseModelStatePrefersConfigOptionsOverLegacy(t *testing.T) {
	raw := json.RawMessage(`{
		"configOptions": [{
			"configId": "model",
			"category": "model",
			"type": "select",
			"currentValue": "new",
			"options": [{"value": "new", "name": "New"}]
		}],
		"models": {
			"currentModelId": "old",
			"availableModels": [{"modelId": "old", "name": "Old"}]
		}
	}`)
	ms := ParseModelState(raw)
	if ms == nil || ms.Current != "new" || ms.ConfigID != "model" {
		t.Fatalf("got %+v", ms)
	}
}

func TestParseModelStateEmpty(t *testing.T) {
	if ParseModelState(nil) != nil {
		t.Fatal("expected nil")
	}
	if ParseModelState(json.RawMessage(`{"sessionId":"x"}`)) != nil {
		t.Fatal("expected nil without model options")
	}
}
