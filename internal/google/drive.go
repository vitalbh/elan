package google

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// File is a Drive file as the rest of elan sees it.
type File struct {
	ID       string
	Name     string
	MimeType string
	Modified time.Time
}

// Drive reads the profile's Google Drive.
type Drive struct {
	svc *drive.Service
}

// NewDrive builds a Drive client on an authorized HTTP client.
func NewDrive(ctx context.Context, hc *http.Client) (*Drive, error) {
	svc, err := drive.NewService(ctx, option.WithHTTPClient(hc))
	if err != nil {
		return nil, err
	}
	return &Drive{svc: svc}, nil
}

// Recent lists the most recently modified files, newest first. query is an
// optional Drive search expression ANDed with the default filter.
func (d *Drive) Recent(ctx context.Context, n int64, query string) ([]File, error) {
	q := "trashed = false"
	if query != "" {
		q += " and (" + query + ")"
	}
	res, err := d.svc.Files.List().
		Context(ctx).
		Q(q).
		OrderBy("modifiedTime desc").
		PageSize(n).
		Fields("files(id,name,mimeType,modifiedTime)").
		Do()
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(res.Files))
	for _, f := range res.Files {
		mod, _ := time.Parse(time.RFC3339, f.ModifiedTime)
		files = append(files, File{ID: f.Id, Name: f.Name, MimeType: f.MimeType, Modified: mod})
	}
	return files, nil
}
