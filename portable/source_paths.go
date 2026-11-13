package portable

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"google.golang.org/api/drive/v3"
)

// sourceFolder reads an exact ancestor, never lists a folder or adds membership.
// Retain its location for the final stability check, separately from inventory.
func (s *Service) sourceFolder(ctx context.Context, id string, cat *catalog) (*drive.File, error) {
	if f := cat.files[id]; f != nil {
		if f.MimeType != folderMIME {
			return nil, fail("ambiguous_target", "relative traversal requires a folder")
		}
		return f, nil
	}
	if f := cat.ancestors[id]; f != nil {
		return f, nil
	}
	if len(cat.ancestors) >= cat.ancestorLimit {
		return nil, fail("limit_exceeded", "source ancestry metadata limit exceeded")
	}
	if e := s.requireProvider(ctx); e != nil {
		return nil, e
	}
	f, e := s.provider.File(ctx, id)
	if e != nil {
		return nil, safeProviderError(e)
	}
	if e = validateFile(f, id); e != nil {
		return nil, e
	}
	if f.MimeType != folderMIME {
		return nil, fail("ambiguous_target", "source ancestor is not a folder")
	}
	if len(f.Parents) > 1 {
		return nil, fail("ambiguous_target", "source ancestor has multiple parents")
	}
	cat.ancestors[id] = f
	return f, nil
}

func (s *Service) resolveRelative(ctx context.Context, target string, source Evidence, cat *catalog) (string, error) {
	u, e := url.Parse(target)
	if e != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "\\") {
		return "", fail("missing_asset", "unsupported or ambiguous relative reference")
	}
	if source.Source.Kind == "local" {
		return "", fail("missing_asset", "local index references must use explicit Drive file links")
	}
	if len(source.Parents) != 1 {
		return "", fail("ambiguous_target", "source needs exactly one original Drive parent")
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) > 32 {
		return "", fail("limit_exceeded", "relative reference too deep")
	}
	current := source.Parents[0]
	upward := map[string]bool{}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			upward[current] = true
			f, e := s.sourceFolder(ctx, current, cat)
			if e != nil {
				return "", e
			}
			if len(f.Parents) != 1 {
				return "", fail("missing_asset", "relative traversal lacks source ancestry")
			}
			current = f.Parents[0]
			if upward[current] {
				return "", fail("ambiguous_target", "source ancestry contains a cycle")
			}
			continue
		}
		upward = map[string]bool{}
		match := ""
		for _, files := range []map[string]*drive.File{cat.files, cat.ancestors} {
			for id, f := range files {
				if f.Name != part {
					continue
				}
				for _, p := range f.Parents {
					if p == current {
						if match != "" && match != id {
							return "", fail("ambiguous_target", "duplicate original Drive filenames")
						}
						match = id
					}
				}
			}
		}
		if match == "" {
			return "", fail("missing_asset", "relative reference is not in the declared source map")
		}
		current = match
	}
	return current, nil
}

func (s *Service) checkSourceAncestry(ctx context.Context, cat *catalog) error {
	ids := make([]string, 0, len(cat.ancestors))
	for id := range cat.ancestors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		f, e := s.provider.File(ctx, id)
		if e != nil {
			return safeProviderError(e)
		}
		if e = validateFile(f, id); e != nil {
			return e
		}
		if !sameLocation(cat.ancestors[id], f) {
			return fail("unstable_source", "source ancestry changed during export")
		}
	}
	return nil
}
