package contribution

import (
	"context"

	"github.com/goliatone/mdoc/workingdraft"
	"google.golang.org/api/drive/v3"
)

const nativeDoc = "application/vnd.google-apps.document"
const folderMime = "application/vnd.google-apps.folder"
const fileFields = "id,name,mimeType,driveId,trashed,parents,appProperties,version,capabilities(canAddChildren,canEdit,canComment)"

func (s *Service) file(ctx context.Context, id string) (*drive.File, error) {
	if !identifier.MatchString(id) {
		return nil, fail(InvalidInput, "invalid Google file identity")
	}
	f, e := s.drive.Files.Get(id).Fields(fileFields).Context(ctx).Do()
	if e != nil {
		return nil, safe(e)
	}
	if f.Id != id {
		return nil, fail(InvalidInput, "provider file identity differs")
	}
	return f, nil
}
func (s *Service) folder(ctx context.Context, id string) error {
	f, e := s.file(ctx, id)
	if e != nil {
		return e
	}
	if f.Trashed || f.DriveId != "" || f.MimeType != folderMime || f.Capabilities == nil || !f.Capabilities.CanAddChildren {
		return fail(AccessDenied, "destination must be an existing writable My Drive folder")
	}
	return nil
}
func canonicalSource(r workingdraft.SourceRef) (workingdraft.SourceRef, error) {
	p, e := workingdraft.ParseSource(r.DocumentID)
	if e != nil || p.DocumentID != r.DocumentID || r.Provider != "" && r.Provider != workingdraft.ProviderGoogleDocs {
		return r, fail(InvalidInput, "invalid Google Doc identity")
	}
	p.TabID = r.TabID
	if p.TabID != "" {
		q, e := workingdraft.ParseSource("https://docs.google.com/document/d/" + p.DocumentID + "/edit?tab=" + p.TabID)
		if e != nil || q.TabID != p.TabID {
			return r, fail(InvalidInput, "invalid tab identity")
		}
	}
	return p, nil
}
func (s *Service) documentAccess(ctx context.Context, r workingdraft.SourceRef, reply bool) error {
	_, e := canonicalSource(r)
	if e != nil {
		return e
	}
	f, e := s.file(ctx, r.DocumentID)
	if e != nil {
		return e
	}
	if f.Trashed || f.DriveId != "" || f.MimeType != nativeDoc || f.Capabilities == nil {
		return fail(AccessDenied, "source must be an accessible native My Drive Doc")
	}
	if reply && !f.Capabilities.CanComment || !reply && !f.Capabilities.CanEdit {
		return fail(AccessDenied, "account lacks the required document capability")
	}
	return nil
}

func validPlanSource(r workingdraft.SourceRef) bool {
	v, e := canonicalSource(r)
	return e == nil && v == r && r.TabID != ""
}
