package render

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

type HashInput struct {
	Source             []byte
	Assets             map[string][]byte
	ReferenceDOCX      []byte
	ProfileFingerprint []byte
	LinkTargets        map[string]string
	PandocVersion      string
	RendererVersion    string
	Reader             string
	HeadingFilter      string
	Filters            []FilterInput
	Parts              []HashPart
}

type HashPart struct {
	Name string
	Data []byte
}

type HashPartDigest struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

type FilterInput struct {
	Path string
	Data []byte
}

func ComputeHash(input HashInput) string {
	hash := sha256.New()
	writeHashPart(hash, "source", input.Source)
	writeHashPart(hash, "reference", input.ReferenceDOCX)
	writeHashPart(hash, "profile", input.ProfileFingerprint)
	writeHashPart(hash, "pandoc", []byte(input.PandocVersion))
	writeHashPart(hash, "renderer", []byte(input.RendererVersion))
	writeHashPart(hash, "reader", []byte(input.Reader))
	writeHashPart(hash, "heading_filter", []byte(input.HeadingFilter))
	for index, filter := range input.Filters {
		writeHashPart(hash, fmt.Sprintf("filter:%d:%s", index, filter.Path), filter.Data)
	}
	assetKeys := sortedKeys(input.Assets)
	for _, key := range assetKeys {
		writeHashPart(hash, "asset:"+key, input.Assets[key])
	}
	linkKeys := make([]string, 0, len(input.LinkTargets))
	for key := range input.LinkTargets {
		linkKeys = append(linkKeys, key)
	}
	sort.Strings(linkKeys)
	for _, key := range linkKeys {
		writeHashPart(hash, "link:"+key, []byte(input.LinkTargets[key]))
	}
	for index, part := range input.Parts {
		writeHashPart(hash, fmt.Sprintf("part:%d:%s", index, part.Name), part.Data)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func DescribeHashInput(input HashInput) []HashPartDigest {
	parts := []HashPart{{Name: "source", Data: input.Source}, {Name: "reference", Data: input.ReferenceDOCX}, {Name: "profile", Data: input.ProfileFingerprint}, {Name: "pandoc", Data: []byte(input.PandocVersion)}, {Name: "renderer", Data: []byte(input.RendererVersion)}, {Name: "reader", Data: []byte(input.Reader)}, {Name: "heading_filter", Data: []byte(input.HeadingFilter)}}
	for index, filter := range input.Filters {
		parts = append(parts, HashPart{Name: fmt.Sprintf("filter:%d:%s", index, filter.Path), Data: filter.Data})
	}
	for _, key := range sortedKeys(input.Assets) {
		parts = append(parts, HashPart{Name: "asset:" + key, Data: input.Assets[key]})
	}
	linkKeys := make([]string, 0, len(input.LinkTargets))
	for key := range input.LinkTargets {
		linkKeys = append(linkKeys, key)
	}
	sort.Strings(linkKeys)
	for _, key := range linkKeys {
		parts = append(parts, HashPart{Name: "link:" + key, Data: []byte(input.LinkTargets[key])})
	}
	for index, part := range input.Parts {
		parts = append(parts, HashPart{Name: fmt.Sprintf("part:%d:%s", index, part.Name), Data: part.Data})
	}
	result := make([]HashPartDigest, 0, len(parts))
	for _, part := range parts {
		digest := sha256.Sum256(part.Data)
		result = append(result, HashPartDigest{Name: part.Name, Hash: fmt.Sprintf("sha256:%x", digest)})
	}
	return result
}

func ComputeArtifactHash(renderHash string, frozenValues any, normalizedDOCX []byte) (string, error) {
	frozen, err := json.Marshal(frozenValues)
	if err != nil {
		return "", fmt.Errorf("encode frozen artifact values: %w", err)
	}
	hash := sha256.New()
	writeHashPart(hash, "render_hash", []byte(renderHash))
	writeHashPart(hash, "frozen_values", frozen)
	writeHashPart(hash, "normalized_docx", normalizedDOCX)
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeHashPart(writer hashWriter, name string, value []byte) {
	fmt.Fprintf(writer, "%d:%s:%d:", len(name), name, len(value))
	writer.Write(value)
}

func sortedKeys(input map[string][]byte) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
