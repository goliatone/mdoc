package workingdraft

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

func versionString(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}
func digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fail(SnapshotCorrupt, "cannot encode snapshot or content")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func ContentDigest(content Content) string { value, _ := digest(content); return value }
func seal(s *Snapshot) error {
	s.ContentDigest = ContentDigest(s.NormalizedContent)
	s.SnapshotDigest = ""
	value, err := digest(*s)
	if err != nil {
		return err
	}
	s.SnapshotDigest = value
	return nil
}
func VerifySnapshot(s Snapshot) error {
	canonical, err := canonicalSource(s.Source)
	if err != nil || canonical != s.Source || s.BodyUsable && s.Source.TabID == "" || s.SchemaVersion != SchemaVersion || s.ConversionVersion != ConversionVersion || s.RetrievedAt.IsZero() || s.SourceURL != sourceURL(s.Source) || !json.Valid(s.RawContent) || s.BodyUsable && (s.Title != s.NormalizedContent.Title || len(s.Diagnostics) != 0) || !s.BodyUsable && s.NormalizedContent != (Content{}) {
		return fail(SnapshotCorrupt, "invalid snapshot envelope")
	}
	want := s.SnapshotDigest
	content := s.ContentDigest
	if err := seal(&s); err != nil {
		return err
	}
	if want != s.SnapshotDigest || content != s.ContentDigest {
		return fail(SnapshotCorrupt, "snapshot integrity digest mismatch")
	}
	return nil
}
func SaveSnapshot(ctx context.Context, store SnapshotStore, s Snapshot) error {
	if err := VerifySnapshot(s); err != nil {
		return err
	}
	if store == nil {
		return fail(SnapshotCorrupt, "snapshot store is required")
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fail(SnapshotCorrupt, "cannot encode snapshot")
	}
	if err := store.Put(ctx, s.SnapshotDigest, data); err != nil {
		return fail(SnapshotCorrupt, "cannot persist snapshot")
	}
	return nil
}
func LoadSnapshot(ctx context.Context, store SnapshotStore, key string) (Snapshot, error) {
	if store == nil {
		return Snapshot{}, fail(SnapshotCorrupt, "snapshot store is required")
	}
	data, err := store.Get(ctx, key)
	if err != nil {
		return Snapshot{}, fail(SnapshotCorrupt, "cannot retrieve snapshot")
	}
	s, err := DecodeSnapshot(data)
	if err != nil {
		return Snapshot{}, err
	}
	if s.SnapshotDigest != key {
		return Snapshot{}, fail(SnapshotCorrupt, "stored snapshot identity mismatch")
	}
	return s, nil
}
func DecodeSnapshot(data []byte) (Snapshot, error) {
	var s Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return Snapshot{}, fail(SnapshotCorrupt, "invalid snapshot JSON")
	}
	if !json.Valid(data) {
		return Snapshot{}, fail(SnapshotCorrupt, "invalid snapshot JSON")
	}
	if err := VerifySnapshot(s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
