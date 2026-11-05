package google

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

const (
	mimeFolder   = "application/vnd.google-apps.folder"
	mimeDocument = "application/vnd.google-apps.document"
	mimeShortcut = "application/vnd.google-apps.shortcut"
)

// MeetFolderName is the folder Meet creates at the root of My Drive.
const MeetFolderName = "Google Meet"

// ErrNoMeetFolder means My Drive has no Meet folder.
var ErrNoMeetFolder = errors.New("pasta \"" + MeetFolderName + "\" não encontrada na raiz do Drive")

// MeetDoc is a Google Doc inside a Meet folder, with shortcuts resolved.
type MeetDoc struct {
	ID         string
	Name       string
	FolderID   string
	FolderName string
	Modified   time.Time
}

// MeetFolderID finds the Meet folder at the root of My Drive.
func (d *Drive) MeetFolderID(ctx context.Context) (string, error) {
	q := fmt.Sprintf("'root' in parents and name = '%s' and mimeType = '%s' and trashed = false", MeetFolderName, mimeFolder)
	res, err := d.svc.Files.List().Context(ctx).Q(q).PageSize(2).Fields("files(id)").Do()
	if err != nil {
		return "", err
	}
	if len(res.Files) == 0 {
		return "", ErrNoMeetFolder
	}
	return res.Files[0].Id, nil
}

// MeetDocs lists the Google Docs in each subfolder of the Meet folder (one
// subfolder per meeting or recurring series). Shortcuts are followed to the
// real doc, which is listed once even when several shortcuts point to it.
// Shortcuts whose target is gone or not shared are skipped.
func (d *Drive) MeetDocs(ctx context.Context, meetFolderID string) ([]MeetDoc, error) {
	folders, err := d.children(ctx, meetFolderID, "files(id,name,mimeType)")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var docs []MeetDoc
	for _, folder := range folders {
		if folder.MimeType != mimeFolder {
			continue
		}
		files, err := d.children(ctx, folder.Id, "files(id,name,mimeType,modifiedTime,shortcutDetails(targetId,targetMimeType))")
		if err != nil {
			return nil, fmt.Errorf("pasta %q: %w", folder.Name, err)
		}
		for _, f := range files {
			doc, ok, err := d.resolve(ctx, f)
			if err != nil {
				return nil, err
			}
			if !ok || seen[doc.ID] {
				continue
			}
			seen[doc.ID] = true
			doc.FolderID, doc.FolderName = folder.Id, folder.Name
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

func (d *Drive) resolve(ctx context.Context, f *drive.File) (MeetDoc, bool, error) {
	switch {
	case f.MimeType == mimeDocument:
		return MeetDoc{ID: f.Id, Name: f.Name, Modified: parseTime(f.ModifiedTime)}, true, nil
	case f.MimeType == mimeShortcut && f.ShortcutDetails != nil && f.ShortcutDetails.TargetMimeType == mimeDocument:
		t, err := d.svc.Files.Get(f.ShortcutDetails.TargetId).Context(ctx).Fields("id,name,modifiedTime,trashed").Do()
		if isNoAccess(err) {
			return MeetDoc{}, false, nil
		}
		if err != nil {
			return MeetDoc{}, false, err
		}
		if t.Trashed {
			return MeetDoc{}, false, nil
		}
		return MeetDoc{ID: t.Id, Name: t.Name, Modified: parseTime(t.ModifiedTime)}, true, nil
	default:
		return MeetDoc{}, false, nil
	}
}

// ExportText exports a Google Doc as plain text.
func (d *Drive) ExportText(ctx context.Context, id string) (string, error) {
	res, err := d.svc.Files.Export(id, "text/plain").Context(ctx).Download()
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 10<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d *Drive) children(ctx context.Context, parent, fields string) ([]*drive.File, error) {
	var out []*drive.File
	call := d.svc.Files.List().Context(ctx).
		Q(fmt.Sprintf("'%s' in parents and trashed = false", parent)).
		PageSize(1000).
		Fields(googleapi.Field("nextPageToken," + fields))
	err := call.Pages(ctx, func(page *drive.FileList) error {
		out = append(out, page.Files...)
		return nil
	})
	return out, err
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// isNoAccess reports a target that is gone or no longer shared.
func isNoAccess(err error) bool {
	var ge *googleapi.Error
	return errors.As(err, &ge) && (ge.Code == http.StatusNotFound || ge.Code == http.StatusForbidden)
}
