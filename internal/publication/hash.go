package publication

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"os"

	"github.com/goliatone/mdoc/internal/render"
)

type HashFile struct {
	Label string
	Path  string
	Data  []byte
}

type RenderPartOptions struct {
	Fields          any
	FieldFiles      []HashFile
	StableComputed  any
	Assets          []HashFile
	ExternalTargets map[string]string
}

func SourceHash(target *Publication) (string, error) {
	if target == nil {
		return "", fmt.Errorf("publication is required")
	}
	digest := sha256.New()
	writePart(digest, "publication_id", []byte(target.ID))
	writePart(digest, "publication_kind", []byte(target.Kind))
	switch target.Kind {
	case KindSource:
		if target.Source == nil {
			return "", fmt.Errorf("source publication %q has no source", target.ID)
		}
		data, err := os.ReadFile(target.Source.Path)
		if err != nil {
			return "", fmt.Errorf("read publication %q source: %w", target.ID, err)
		}
		writePart(digest, "source_key", []byte(target.Source.SourceKey))
		writePart(digest, "source_bytes", data)
	case KindBundle:
		for index, member := range target.Members {
			data, err := os.ReadFile(member.Document.Path)
			if err != nil {
				return "", fmt.Errorf("read publication %q member %q: %w", target.ID, member.SourceKey, err)
			}
			writePart(digest, fmt.Sprintf("member:%d:key", index), []byte(member.SourceKey))
			writePart(digest, fmt.Sprintf("member:%d:source", index), data)
		}
	default:
		return "", fmt.Errorf("publication %q has unsupported kind %q", target.ID, target.Kind)
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil)), nil
}

func RenderHashParts(target *Publication, sourceHash string, options RenderPartOptions) ([]render.HashPart, error) {
	if target == nil {
		return nil, fmt.Errorf("publication is required")
	}
	type memberHashConfig struct {
		SourceKey string `json:"source_key"`
		Config    any    `json:"config"`
	}
	members := make([]memberHashConfig, 0, len(target.Members))
	for _, member := range target.Members {
		members = append(members, memberHashConfig{SourceKey: member.SourceKey, Config: member.Config})
	}
	publicationConfig, err := json.Marshal(struct {
		ID             string `json:"id"`
		Kind           Kind   `json:"kind"`
		Title          string `json:"title"`
		Layout         any    `json:"layout"`
		Members        any    `json:"members"`
		ReferenceLinks any    `json:"reference_links"`
		FragmentPolicy string `json:"fragment_policy"`
		HeadingPolicy  string `json:"heading_policy"`
	}{target.ID, target.Kind, target.Title, target.Layout, members, target.ReferenceLinks, target.FragmentPolicy, target.HeadingPolicy})
	if err != nil {
		return nil, fmt.Errorf("encode publication hash config: %w", err)
	}
	fields, err := json.Marshal(options.Fields)
	if err != nil {
		return nil, fmt.Errorf("encode resolved fields: %w", err)
	}
	computed, err := json.Marshal(options.StableComputed)
	if err != nil {
		return nil, fmt.Errorf("encode stable computed values: %w", err)
	}
	parts := []render.HashPart{{Name: "bundle_source_hash", Data: []byte(sourceHash)}, {Name: "resolved_fields", Data: fields}, {Name: "stable_computed", Data: computed}, {Name: "publication_config", Data: publicationConfig}}
	for index, file := range options.FieldFiles {
		data, readErr := hashFileData(file)
		if readErr != nil {
			return nil, fmt.Errorf("read field file %s: %w", file.Label, readErr)
		}
		parts = append(parts, render.HashPart{Name: fmt.Sprintf("field_file:%d:%s", index, file.Label), Data: data})
	}
	for index, asset := range options.Assets {
		data, readErr := os.ReadFile(asset.Path)
		if readErr != nil {
			return nil, fmt.Errorf("read asset %s: %w", asset.Path, readErr)
		}
		parts = append(parts, render.HashPart{Name: fmt.Sprintf("bundle_asset:%d:%s", index, asset.Label), Data: data})
	}
	targets, err := json.Marshal(options.ExternalTargets)
	if err != nil {
		return nil, fmt.Errorf("encode external targets: %w", err)
	}
	parts = append(parts, render.HashPart{Name: "external_targets", Data: targets})
	return parts, nil
}

func hashFileData(file HashFile) ([]byte, error) {
	if file.Data != nil {
		return file.Data, nil
	}
	return os.ReadFile(file.Path)
}

func writePart(writer hash.Hash, name string, value []byte) {
	fmt.Fprintf(writer, "%d:%s:%d:", len(name), name, len(value))
	_, _ = writer.Write(value)
}
