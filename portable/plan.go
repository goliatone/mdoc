package portable

import (
	"context"
	"google.golang.org/api/drive/v3"
	"path"
	"sort"
	"strings"
)

type catalog struct {
	files map[string]*drive.File
	// linkPaths chooses one deterministic navigation target per source. It is
	// never used to decide which copies must be installed.
	linkPaths  map[string]string
	treeAssets map[string][]assetDestination
	// Ancestors supply source context only; they never add bundle members.
	ancestors     map[string]*drive.File
	ancestorLimit int
}

type assetDestination struct {
	FileID string
	Path   string
}

func newCatalog() *catalog {
	return &catalog{files: map[string]*drive.File{}, linkPaths: map[string]string{}, treeAssets: map[string][]assetDestination{}, ancestors: map[string]*drive.File{}, ancestorLimit: 32}
}
func (s *Service) planModel(ctx context.Context, c Config, m map[string]any) (Plan, *catalog, error) {
	entries, e := parseInventory(m, c)
	if e != nil {
		return Plan{}, nil, e
	}
	p, e := resolveLayout(c, entries)
	if e != nil {
		return p, nil, e
	}
	cat := newCatalog()
	cat.ancestorLimit = (c.Limits.Documents + c.Limits.Assets) * c.Limits.Depth
	if e = s.requireProvider(ctx); e != nil {
		return p, nil, e
	}
	count := 0
	for i, entry := range p.Entries {
		s.sourceAccess(entry.Source)
		p.Entries[i].Source = publicSourceLink(entry.Source)
		if entry.Role == "reference" {
			continue
		}
		ref, _ := ParseSource(entry.Source)
		f, e := s.provider.File(ctx, ref.FileID)
		if e != nil {
			return p, nil, safeProviderError(e)
		}
		if e = validateFile(f, ref.FileID); e != nil {
			return p, nil, e
		}
		cat.files[f.Id] = f
		switch entry.Role {
		case "document":
			if f.MimeType != docMIME && f.MimeType != "text/markdown" && f.MimeType != "text/x-markdown" && !(f.MimeType == "text/plain" && strings.HasSuffix(strings.ToLower(f.Name), ".md")) {
				return p, nil, fail("unsupported_content", "%s is not a supported document", entry.ID)
			}
		case "asset":
			if strings.HasPrefix(f.MimeType, "application/vnd.google-apps.") {
				return p, nil, fail("unsupported_content", "%s is not a downloadable asset", entry.ID)
			}
			count++
		case "asset-tree":
			if f.MimeType != folderMIME {
				return p, nil, fail("invalid_index", "%s is not a folder", entry.ID)
			}
			seen := map[string]bool{}
			if e = s.listTree(ctx, c, cat, &p, entry.ID, f.Id, entry.Path, 0, &count, seen); e != nil {
				return p, nil, e
			}
		}
	}
	// Explicit inventory paths take precedence for navigation regardless of row
	// order. All tree copies still retain their own destinations.
	for _, entry := range p.Entries {
		ref, _ := ParseSource(entry.Source)
		switch entry.Role {
		case "document", "asset":
			cat.linkPaths[ref.FileID] = entry.Path
		case "asset-tree":
			cat.linkPaths[ref.FileID] = listingPath(c, entry)
		}
	}
	if count > c.Limits.Assets {
		return p, nil, fail("limit_exceeded", "too many declared assets")
	}
	p.Diagnostics = append(p.Diagnostics, Diagnostic{Code: "visible_inventory", Severity: "info", Message: "Folder inventory reflects files visible to this account; referenced files require independent access."})
	return p, cat, nil
}
func (s *Service) listTree(ctx context.Context, c Config, cat *catalog, p *Plan, treeID, id, dest string, depth int, count *int, seen map[string]bool) error {
	if depth > c.Limits.Depth || seen[id] {
		return fail("limit_exceeded", "asset tree depth or cycle limit exceeded")
	}
	seen[id] = true
	cursor := ""
	pages := map[string]bool{}
	files := []*drive.File{}
	for {
		if pages[cursor] {
			return fail("unstable_source", "folder pagination repeated")
		}
		pages[cursor] = true
		if len(pages) > c.Limits.Assets+1 {
			return fail("limit_exceeded", "folder pagination limit exceeded")
		}
		page, e := s.provider.Children(ctx, id, cursor)
		if e != nil {
			return safeProviderError(e)
		}
		if page == nil || page.IncompleteSearch {
			return fail("access_denied", "folder search is incomplete")
		}
		if len(files)+len(page.Files) > c.Limits.Assets {
			return fail("limit_exceeded", "folder count limit exceeded")
		}
		for _, f := range page.Files {
			if f == nil {
				return fail("invalid_source", "folder contains an invalid file record")
			}
			if e := validateFile(f, f.Id); e != nil {
				return e
			}
			foundParent := false
			for _, p := range f.Parents {
				foundParent = foundParent || p == id
			}
			if !foundParent {
				return fail("unstable_source", "folder child has mismatched ancestry")
			}
		}
		files = append(files, page.Files...)
		cursor = page.NextPageToken
		if cursor == "" {
			break
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	names := map[string]bool{}
	for _, f := range files {
		if e := validateFile(f, f.Id); e != nil {
			return e
		}
		if names[fold(f.Name)] || strings.Contains(f.Name, "/") {
			return fail("ambiguous_target", "duplicate or unsafe folder filename")
		}
		names[fold(f.Name)] = true
		if e := safePath(f.Name); e != nil {
			return e
		}
		child := path.Join(dest, f.Name)
		cat.files[f.Id] = f
		*count++
		if *count > c.Limits.Assets {
			return fail("limit_exceeded", "asset count exceeded")
		}
		if f.MimeType == folderMIME {
			if e := s.listTree(ctx, c, cat, p, treeID, f.Id, child, depth+1, count, seen); e != nil {
				return e
			}
		} else {
			if strings.HasPrefix(f.MimeType, "application/vnd.google-apps.") {
				return fail("unsupported_content", "asset tree contains an unsupported native file")
			}
			if e := reserve(&p.Destinations, Reservation{Path: child, ID: f.Id, Kind: "asset"}); e != nil {
				return e
			}
			cat.treeAssets[treeID] = append(cat.treeAssets[treeID], assetDestination{FileID: f.Id, Path: child})
			if prior := cat.linkPaths[f.Id]; prior == "" || child < prior {
				cat.linkPaths[f.Id] = child
			}
		}
	}
	return nil
}
func (s *Service) Plan(ctx context.Context, r PlanRequest) (Plan, error) {
	if e := r.Config.Validate(); e != nil {
		return Plan{}, e
	}
	b := &budget{max: r.Config.Limits.TotalBytes}
	d, e := s.readDraft(ctx, r.Config.Index.Source, r.Config, b)
	if e != nil {
		return Plan{}, e
	}
	p, _, e := s.planModel(ctx, r.Config, d.model)
	p.IndexDigest = d.evidence.SourceDigest
	return p, e
}
