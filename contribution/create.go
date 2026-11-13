package contribution

import (
	"bytes"
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

func (s *Service) PrepareCreate(ctx context.Context, in CreateInput) (CreatePlan, error) {
	if e := s.meta(in.Meta); e != nil {
		return CreatePlan{}, e
	}
	if in.Title == "" || strings.TrimSpace(in.Title) == "" || len(in.Title) > 1024 || !utf8.ValidString(in.Title) || len(in.Markdown) > MaxMarkdown || !utf8.ValidString(in.Markdown) {
		return CreatePlan{}, fail(InvalidInput, "invalid title or Markdown")
	}
	if e := s.folder(ctx, in.ParentID); e != nil {
		return CreatePlan{}, e
	}
	renderer := s.options.Renderer
	if renderer == nil {
		renderer = pandocRenderer{}
	} else {
		if _, e := supportedMarkdown(ctx, "", in.Markdown); e != nil {
			return CreatePlan{}, e
		}
	}
	artifact, e := renderer.Render(ctx, in.Markdown)
	if e != nil {
		return CreatePlan{}, fail(UnsupportedContent, "document rendering failed")
	}
	artifact.DOCX = append([]byte(nil), artifact.DOCX...)
	artifact.Digest = hash(artifact.DOCX)
	if e := validateArtifact(artifact); e != nil {
		return CreatePlan{}, e
	}
	p := CreatePlan{Kind: CreateKind, Input: in, Artifact: artifact}
	p.Digest = createDigest(p)
	return p, validateCreate(p)
}
func properties(p CreatePlan) map[string]string {
	return map[string]string{"mdoc_c_namespace": hash([]byte(p.Input.Meta.Namespace)), "mdoc_c_operation": hash([]byte(p.Input.Meta.OperationID)), "mdoc_c_request": p.Digest, "mdoc_c_artifact": p.Artifact.Digest}
}
func (s *Service) findCreate(ctx context.Context, p CreatePlan) (*drive.File, error) {
	props := properties(p)
	q := "appProperties has { key='mdoc_c_namespace' and value='" + props["mdoc_c_namespace"] + "' } and appProperties has { key='mdoc_c_operation' and value='" + props["mdoc_c_operation"] + "' }"
	token := ""
	seen := map[string]bool{}
	var found *drive.File
	for page := 0; page < 100; page++ {
		r, e := s.drive.Files.List().Q(q).Spaces("drive").PageSize(100).PageToken(token).Fields("nextPageToken,incompleteSearch,files(" + googleapi.Field(fileFields) + ")").Context(ctx).Do()
		if e != nil {
			return nil, safe(e)
		}
		if r.IncompleteSearch {
			return nil, fail(LimitExceeded, "creation discovery incomplete")
		}
		for _, f := range r.Files {
			if f == nil || !identifier.MatchString(f.Id) || found != nil || f.MimeType != nativeDoc || f.DriveId != "" || len(f.Parents) != 1 || f.Parents[0] != p.Input.ParentID {
				return nil, fail(Stale, "creation identity is ambiguous or moved")
			}
			for k, v := range props {
				if f.AppProperties[k] != v {
					return nil, fail(Stale, "creation metadata differs")
				}
			}
			found = f
		}
		if r.NextPageToken == "" {
			return found, nil
		}
		if seen[r.NextPageToken] {
			break
		}
		seen[r.NextPageToken] = true
		token = r.NextPageToken
	}
	return nil, fail(LimitExceeded, "creation discovery incomplete")
}
func (s *Service) Create(ctx context.Context, r CreateRequest) (Outcome, error) {
	p := r.Plan
	o := outcome(CreateKind, p.Input.Meta, p.Digest, r.Attempt)
	if e := validateCreate(p); e != nil {
		return rejected(o, e)
	}
	if e := s.authorize(p.Input.Meta, p.Digest, r.ExpectedDigest, r.Attempt); e != nil {
		return rejected(o, e)
	}
	if e := validateArtifact(p.Artifact); e != nil {
		return rejected(o, e)
	}
	found, e := s.findCreate(ctx, p)
	if e != nil {
		return rejected(o, e)
	}
	if found != nil {
		o.DocumentID = found.Id
		o = s.committed(o)
		o.AcknowledgedAt = time.Time{}
		o.Diagnostics = []string{"creation recovered from exact operation metadata"}
		if found.Trashed {
			o.Diagnostics = append(o.Diagnostics, "document is now trashed")
		}
		return o, nil
	}
	if e := s.folder(ctx, p.Input.ParentID); e != nil {
		return rejected(o, e)
	}
	if ctx.Err() != nil {
		return rejected(o, ctx.Err())
	}
	f, e := s.drive.Files.Create(&drive.File{Name: p.Input.Title, MimeType: nativeDoc, Parents: []string{p.Input.ParentID}, AppProperties: properties(p)}).Media(bytes.NewReader(p.Artifact.DOCX), googleapi.ContentType("application/vnd.openxmlformats-officedocument.wordprocessingml.document"), googleapi.ChunkSize(0)).Fields("id").Context(ctx).Do()
	if e != nil {
		return mutationError(o, e)
	}
	if f == nil || !identifier.MatchString(f.Id) {
		o.Status = Unknown
		return rejected(o, fail(ReconciliationRequired, "create acknowledgment omitted document identity"))
	}
	o.DocumentID = f.Id
	o = s.committed(o)
	o.Diagnostics = []string{"content and revision hydration require separate inspection"}
	return o, nil
}
