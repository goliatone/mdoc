package portable

import (
	"context"
	"encoding/json"
	"google.golang.org/api/drive/v3"
	"os"
	"slices"
	"sort"
	"strings"
)

type assembly struct {
	files        map[string][]byte
	manifest     Manifest
	reservations []Reservation
	assets       map[string]capturedAsset
	assetIndices map[string]int
	observations map[string]capturedAsset
}

func newAssembly(c Config, now Evidence) *assembly {
	return &assembly{files: map[string][]byte{}, manifest: Manifest{SchemaVersion: 1, Project: c.Project, ConfigDigest: configDigest(c), ConversionVersion: ConversionVersion, CapturedFrom: now.CapturedFrom}, assets: map[string]capturedAsset{}, assetIndices: map[string]int{}, observations: map[string]capturedAsset{}}
}
func (a *assembly) addAsset(asset capturedAsset, occurrences []Occurrence) error {
	if asset.evidence.Source.Kind == "drive" {
		id := asset.evidence.Source.FileID
		if prior, ok := a.observations[id]; ok && (!sameEvidence(prior.evidence, asset.evidence) || prior.digest != asset.digest || coalesceAsset(prior, asset) != nil) {
			return fail("unstable_source", "conflicting finalized observations of Drive asset %s", id)
		}
		a.observations[id] = asset
	}
	if existing, ok := a.assets[asset.path]; ok {
		if e := coalesceAsset(existing, asset); e != nil {
			return e
		}
		idx := a.assetIndices[asset.path]
		a.manifest.Assets[idx].Sources = append(a.manifest.Assets[idx].Sources, asset.evidence)
		a.manifest.Assets[idx].Occurrences = append(a.manifest.Assets[idx].Occurrences, occurrences...)
		return nil
	}
	if e := reserve(&a.reservations, Reservation{Path: asset.path, Kind: "asset"}); e != nil {
		return e
	}
	a.assets[asset.path] = asset
	a.files[asset.path] = asset.bytes
	a.assetIndices[asset.path] = len(a.manifest.Assets)
	a.manifest.Assets = append(a.manifest.Assets, AssetRecord{Path: asset.path, MIME: asset.mime, Digest: asset.digest, Size: int64(len(asset.bytes)), Sources: []Evidence{asset.evidence}, Occurrences: occurrences})
	return nil
}
func (a *assembly) addFile(p string, b []byte, kind string) error {
	if e := reserve(&a.reservations, Reservation{Path: p, Kind: kind}); e != nil {
		return e
	}
	a.files[p] = b
	return nil
}
func (s *Service) Export(ctx context.Context, r ExportRequest) (Result, error) {
	result := Result{Status: "incomplete"}
	c := r.Config
	if e := c.Validate(); e != nil {
		return result, e
	}
	if r.Destination == "" {
		return result, fail("invalid_config", "explicit destination required")
	}
	if _, e := os.Lstat(r.Destination); e == nil {
		return result, fail("existing_output", "destination already exists")
	} else if !os.IsNotExist(e) {
		return result, e
	}
	transfer := &budget{max: c.Limits.TotalBytes}
	indexMaterializer := &assetMaterializer{s: s, c: c, transfer: transfer, index: true}
	index, e := s.capture(ctx, c.Index.Source, c, transfer, indexMaterializer)
	if e != nil {
		return result, e
	}
	p, cat, e := s.planModel(ctx, c, index.model)
	if e != nil {
		return result, e
	}
	for id, f := range indexMaterializer.cat.ancestors {
		cat.ancestors[id] = f
	}
	if index.seal == nil {
		return result, fail("capture_integrity", "index is not finalized")
	}
	p.IndexDigest = index.evidence.SourceDigest
	canonicalIndexBytes, _ := json.Marshal(index.model)
	docs := map[string]*localDocument{}
	ordered := []*localDocument{{id: "index", path: c.Layout.Index, capture: index}}
	docs[index.evidence.Source.FileID] = ordered[0]
	paths := map[string]string{index.evidence.Source.FileID: c.Layout.Index}
	for _, entry := range p.Entries {
		ref, _ := ParseSource(entry.Source)
		if ref.FileID == index.evidence.Source.FileID && index.evidence.Source.Kind != "local" {
			return result, fail("duplicate_identity", "index cannot also be a member document")
		}
		switch entry.Role {
		case "document":
			cap, e := s.capture(ctx, entry.Source, c, transfer, &assetMaterializer{s: s, c: c, cat: cat, transfer: transfer})
			if e != nil {
				return result, e
			}
			d := &localDocument{id: entry.ID, path: entry.Path, capture: cap}
			docs[ref.FileID] = d
			ordered = append(ordered, d)
			paths[ref.FileID] = entry.Path
		case "asset":
			paths[ref.FileID] = entry.Path
		case "asset-tree":
			paths[ref.FileID] = listingPath(c, entry)
		}
	}
	for id, p := range cat.linkPaths {
		if cat.files[id].MimeType != folderMIME {
			paths[id] = p
		}
	}
	assembly := newAssembly(c, index.evidence)
	if e = reserve(&assembly.reservations, Reservation{Path: "manifest.json", Kind: "manifest"}); e != nil {
		return result, e
	}
	sources := map[SourceRef]bool{}
	occurrences := 0
	for _, d := range ordered {
		if d.capture.seal == nil {
			return result, fail("capture_integrity", "unfinalized document cannot be assembled")
		}
		if e = s.prepareAnchors(ctx, d); e != nil {
			return result, e
		}
		for i, asset := range d.capture.assets {
			dest, e := assetPath(c, d.path, asset)
			if e != nil {
				return result, e
			}
			d.capture.assets[i].path = dest
		}
		for _, a := range d.capture.assets {
			sources[a.evidence.Source] = true
		}
		occurrences += len(d.capture.occurrences)
	}
	fillInventoryPaths(ordered[0].capture.model, p, c.Index.InventoryHeading)
	for _, d := range ordered {
		b, links, diags, e := s.rewriteDocument(ctx, d, c, docs, paths, cat)
		if e != nil {
			return result, e
		}
		if e = assembly.addFile(d.path, b, "document"); e != nil {
			return result, e
		}
		record := DocumentRecord{ID: d.id, Evidence: d.capture.evidence, Path: d.path, ContentDigest: digest(b)}
		if d == ordered[0] {
			assembly.manifest.Index = record
		} else {
			assembly.manifest.Documents = append(assembly.manifest.Documents, record)
		}
		assembly.manifest.Links = append(assembly.manifest.Links, links...)
		assembly.manifest.Diagnostics = append(assembly.manifest.Diagnostics, diags...)
		for _, asset := range d.capture.assets {
			matched := []Occurrence{}
			for _, o := range d.capture.occurrences {
				if o.ObjectID == asset.objectID {
					matched = append(matched, o)
				}
			}
			if e = assembly.addAsset(asset, matched); e != nil {
				return result, e
			}
		}
	}
	supplements := map[string]capturedAsset{}
	for _, entry := range p.Entries {
		destinations := []assetDestination{}
		if entry.Role == "asset" {
			ref, _ := ParseSource(entry.Source)
			destinations = append(destinations, assetDestination{FileID: ref.FileID, Path: entry.Path})
		} else if entry.Role == "asset-tree" {
			destinations = cat.treeAssets[entry.ID]
			b, e := s.treeListing(ctx, entry, c, cat)
			if e != nil {
				return result, e
			}
			dest := listingPath(c, entry)
			if e = assembly.addFile(dest, b, "generated"); e != nil {
				return result, e
			}
			assembly.manifest.GeneratedFiles = append(assembly.manifest.GeneratedFiles, GeneratedFile{dest, digest(b)})
		}
		for _, dest := range destinations {
			asset, ok := supplements[dest.FileID]
			if !ok {
				asset, e = s.stableBlob(ctx, SourceRef{FileID: dest.FileID, Kind: "drive"}, c, transfer)
				if e != nil {
					return result, e
				}
				supplements[dest.FileID] = asset
			}
			asset.path = dest.Path
			if strings.HasPrefix(asset.mime, "image/") && !imagePathOK(asset.path, asset.mime) {
				return result, fail("path_conflict", "image extension does not match bytes")
			}
			if e = assembly.addAsset(asset, nil); e != nil {
				return result, e
			}
			sources[asset.evidence.Source] = true
		}
	}
	if len(sources) > c.Limits.Assets || occurrences > c.Limits.Assets {
		return result, fail("limit_exceeded", "bundle asset source or occurrence count exceeded")
	}
	// Re-list declared folders before installation; membership/metadata changes must
	// never silently truncate or mix a tree during the capture interval.
	var canonicalIndex map[string]any
	if e = json.Unmarshal(canonicalIndexBytes, &canonicalIndex); e != nil {
		return result, e
	}
	_, after, e := s.planModel(ctx, c, canonicalIndex)
	if e != nil {
		return result, e
	}
	if !sameCatalog(cat, after) {
		return result, fail("unstable_source", "declared inventory changed during export")
	}
	if e = s.checkSourceAncestry(ctx, cat); e != nil {
		return result, e
	}
	if e = assembly.checkPlan(p); e != nil {
		return result, e
	}
	assembly.manifest.CapturedTo = s.now().UTC()
	sort.Slice(assembly.manifest.Assets, func(i, j int) bool { return assembly.manifest.Assets[i].Path < assembly.manifest.Assets[j].Path })
	verification, e := s.verifyFiles(ctx, assembly.manifest, assembly.files)
	if e != nil {
		return result, e
	}
	assembly.manifest.LocalComplete = verification.LocalComplete
	assembly.manifest.NoOnlineDependencies = verification.NoOnlineDependencies
	assembly.manifest.Diagnostics = append(assembly.manifest.Diagnostics, verification.Diagnostics...)
	mb, e := json.MarshalIndent(assembly.manifest, "", "  ")
	if e != nil {
		return result, e
	}
	assembly.files["manifest.json"] = append(mb, '\n')
	var total int64
	for _, b := range assembly.files {
		total += int64(len(b))
	}
	if total > c.Limits.TotalBytes {
		return result, fail("limit_exceeded", "installed bytes exceed configured total")
	}
	if e = s.outputStore().Install(ctx, r.Destination, assembly.files); e != nil {
		return result, e
	}
	return Result{Manifest: assembly.manifest, EntryPath: c.Layout.Index, Status: "complete", Diagnostics: assembly.manifest.Diagnostics}, nil
}
func sameCatalog(a, b *catalog) bool {
	if len(a.files) != len(b.files) || len(a.treeAssets) != len(b.treeAssets) {
		return false
	}
	for id, f := range a.files {
		other := b.files[id]
		if other == nil || !sameLocation(f, other) || a.linkPaths[id] != b.linkPaths[id] {
			return false
		}
	}
	for id, destinations := range a.treeAssets {
		if !slices.Equal(destinations, b.treeAssets[id]) {
			return false
		}
	}
	return true
}

// Verification of the manifest alone cannot detect an omitted planned file if
// both the file and its manifest record were lost during assembly.
func (a *assembly) checkPlan(p Plan) error {
	for _, required := range p.Destinations {
		if required.Path == "manifest.json" {
			continue // Serialized only after content validation.
		}
		if _, ok := a.files[required.Path]; !ok {
			return fail("capture_integrity", "planned output %s was not assembled", required.Path)
		}
	}
	return nil
}

// Capture revisions may advance before stabilizing. The final inventory check
// compares membership and source locations; finalized records own byte revisions.
func sameLocation(a, b *drive.File) bool {
	x := fileEvidence(a, SourceRef{})
	y := fileEvidence(b, SourceRef{})
	x.Version = 0
	y.Version = 0
	return sameEvidence(x, y)
}
