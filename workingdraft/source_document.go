package workingdraft

import (
	"bytes"
	"encoding/json"

	"google.golang.org/api/docs/v1"
)

type sourceDocument struct {
	*docs.Document
	raw        json.RawMessage
	tree       map[string]any
	unknown    bool
	styleError error
}

func prepareDocument(raw json.RawMessage) (*sourceDocument, error) {
	if !json.Valid(raw) {
		return nil, fail(UnsupportedContent, "invalid document representation")
	}
	var tree map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&tree); err != nil || tree == nil {
		return nil, fail(UnsupportedContent, "invalid document representation")
	}
	redactCapabilities(tree)
	retained, err := json.Marshal(tree)
	if err != nil {
		return nil, fail(UnsupportedContent, "document representation cannot be retained")
	}
	var doc docs.Document
	if err := json.Unmarshal(retained, &doc); err != nil {
		return nil, fail(UnsupportedContent, "invalid document representation")
	}
	strict := json.NewDecoder(bytes.NewReader(retained))
	strict.DisallowUnknownFields()
	var checked docs.Document
	unknown := strict.Decode(&checked) != nil
	styleError := resolveTextStyles(&doc, tree)
	return &sourceDocument{Document: &doc, raw: retained, tree: tree, unknown: unknown, styleError: styleError}, nil
}

func redactCapabilities(value any) bool {
	found := false
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "contentUri" {
				delete(v, key)
				found = true
			} else if redactCapabilities(item) {
				found = true
			}
		}
	case []any:
		for _, item := range v {
			if redactCapabilities(item) {
				found = true
			}
		}
	}
	return found
}

func containsCapabilities(raw json.RawMessage) bool {
	var tree any
	if json.Unmarshal(raw, &tree) != nil {
		return true
	}
	return redactCapabilities(tree)
}
