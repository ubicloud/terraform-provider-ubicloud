// Preprocesses the Ubicloud OpenAPI spec for use with tfplugingen-openapi.
// tfplugingen-openapi v0.3.0 does not support allOf with more than one subschema.
// This tool merges allOf-N response schemas into a single flat schema so that
// tfplugingen-openapi can process the spec without errors.
package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) < 3 {
		log.Fatal("Usage: preprocess_openapi <input.yml> <output.json>")
	}

	inputPath := os.Args[1]
	outputPath := os.Args[2]

	data, err := os.ReadFile(inputPath)
	if err != nil {
		log.Fatalf("Error reading %s: %v", inputPath, err)
	}

	var spec map[string]interface{}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		log.Fatalf("Error parsing YAML: %v", err)
	}

	flattenResponseAllOf(spec)

	if err := os.MkdirAll(outputPath[:strings.LastIndex(outputPath, "/")], 0755); err != nil {
		log.Fatalf("Error creating output directory: %v", err)
	}

	jsonData, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		log.Fatalf("Error marshaling JSON: %v", err)
	}

	if err := os.WriteFile(outputPath, jsonData, 0644); err != nil {
		log.Fatalf("Error writing %s: %v", outputPath, err)
	}
}

func resolveRef(spec map[string]interface{}, ref string) (map[string]interface{}, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var current interface{} = spec
	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	result, ok := current.(map[string]interface{})
	return result, ok
}

func deepCopyValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v2 := range val {
			result[k] = deepCopyValue(v2)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = deepCopyValue(item)
		}
		return result
	default:
		return val
	}
}

func flattenResponseAllOf(spec map[string]interface{}) {
	components, ok := spec["components"].(map[string]interface{})
	if !ok {
		return
	}
	responses, ok := components["responses"].(map[string]interface{})
	if !ok {
		return
	}

	for _, v := range responses {
		resp, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := resp["content"].(map[string]interface{})
		if !ok {
			continue
		}
		jsonContent, ok := content["application/json"].(map[string]interface{})
		if !ok {
			continue
		}
		schema, ok := jsonContent["schema"].(map[string]interface{})
		if !ok {
			continue
		}
		allOf, ok := schema["allOf"].([]interface{})
		if !ok || len(allOf) <= 1 {
			continue
		}

		jsonContent["schema"] = mergeAllOf(spec, allOf)
	}
}

func mergeAllOf(spec map[string]interface{}, allOf []interface{}) map[string]interface{} {
	mergedProps := make(map[string]interface{})
	var mergedRequired []string
	seen := make(map[string]bool)

	for _, item := range allOf {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if ref, ok := itemMap["$ref"].(string); ok {
			resolved, ok := resolveRef(spec, ref)
			if !ok {
				continue
			}
			itemMap = resolved
		}

		if props, ok := itemMap["properties"].(map[string]interface{}); ok {
			for k, v := range props {
				if _, exists := mergedProps[k]; !exists {
					mergedProps[k] = deepCopyValue(v)
				}
			}
		}

		if req, ok := itemMap["required"].([]interface{}); ok {
			for _, r := range req {
				if rs, ok := r.(string); ok && !seen[rs] {
					seen[rs] = true
					mergedRequired = append(mergedRequired, rs)
				}
			}
		}
	}

	result := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
	}
	if len(mergedProps) > 0 {
		result["properties"] = mergedProps
	}
	if len(mergedRequired) > 0 {
		result["required"] = mergedRequired
	}
	return result
}
