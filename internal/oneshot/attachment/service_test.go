package attachment

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/opendray/opendray-v2/internal/oneshot/domain"
)

type repoFixture struct {
	item      domain.StagedAttachmentSnapshot
	createErr error
}

func (r *repoFixture) CreateStagedAttachment(_ context.Context, item domain.StagedAttachmentSnapshot) (domain.StagedAttachmentSnapshot, error) {
	if r.createErr != nil {
		return domain.StagedAttachmentSnapshot{}, r.createErr
	}
	r.item = item
	return item, nil
}
func (r *repoFixture) GetStagedAttachment(_ context.Context, _ domain.Owner, _ string) (domain.StagedAttachmentSnapshot, error) {
	return r.item, nil
}
func (r *repoFixture) DeleteStagedAttachment(_ context.Context, _ domain.Owner, _ string, now time.Time) (domain.StagedAttachmentSnapshot, error) {
	item := r.item
	item.Status = domain.StagedAttachmentDeleted
	item.DeletedAt = &now
	return item, nil
}

type storageFixture struct {
	data      map[string][]byte
	deleted   []string
	putErr    error
	deleteErr error
}

func (s *storageFixture) Put(_ context.Context, key string, data []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	s.data[key] = append([]byte(nil), data...)
	return nil
}
func (s *storageFixture) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.data[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *storageFixture) Delete(_ context.Context, key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.data, key)
	s.deleted = append(s.deleted, key)
	return nil
}
func newFixture(t *testing.T, max int64) (*Service, *repoFixture, *storageFixture) {
	t.Helper()
	r := &repoFixture{}
	st := &storageFixture{}
	p := DefaultPolicy()
	p.MaxBytes = max
	s, err := NewService(r, st, p)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC) }
	return s, r, st
}
func request(name, mime string, data []byte) StageRequest {
	return StageRequest{Owner: domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, ProjectID: "project-1", SourceKind: domain.SourceMobile, SourceRef: "request-1", Name: name, DeclaredMIME: mime, Reader: bytes.NewReader(data)}
}
func TestStageAttachmentValidatesContentAndUsesServerKey(t *testing.T) {
	s, r, st := newFixture(t, 1024)
	item, err := s.Stage(context.Background(), request("notes.txt", "text/plain", []byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	if item.ID == "" || item.StorageKey == "" || item.SourceRef != "request-1" {
		t.Fatalf("unexpected item: %+v", item)
	}
	if !bytes.Equal(st.data[item.StorageKey], []byte("hello")) {
		t.Fatal("bytes not persisted")
	}
	if err := s.VerifyStored(context.Background(), domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, r.item.ID); err != nil {
		t.Fatal(err)
	}
}
func TestStageRejectsTraversalOversizeAndMIMESpoof(t *testing.T) {
	s, _, _ := newFixture(t, 4)
	cases := []StageRequest{request("../x.txt", "text/plain", []byte("ok")), request("x.txt", "text/plain", []byte("12345")), request("x.png", "image/png", []byte("plain text")), request("x.exe", "application/octet-stream", []byte{'M', 'Z', 0, 0})}
	for _, tc := range cases {
		if _, err := s.Stage(context.Background(), tc); err == nil {
			t.Fatalf("expected rejection for %+v", tc)
		}
	}
}
func TestMetadataFailureDeletesStagedBytes(t *testing.T) {
	s, r, st := newFixture(t, 1024)
	r.createErr = errors.New("db down")
	if _, err := s.Stage(context.Background(), request("x.txt", "text/plain", []byte("hello"))); err == nil {
		t.Fatal("expected error")
	}
	if len(st.deleted) != 1 || len(st.data) != 0 {
		t.Fatalf("orphaned bytes: %+v", st)
	}
}
func TestSourceURLIsHashed(t *testing.T) {
	s, _, _ := newFixture(t, 1024)
	req := request("x.txt", "text/plain", []byte("hello"))
	req.SourceRef = "https://api.telegram.org/file/botSECRET/a?sig=x"
	item, err := s.Stage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.SourceRef) < 7 || item.SourceRef[:7] != "sha256:" {
		t.Fatalf("secret source ref persisted: %s", item.SourceRef)
	}
}

func TestDeleteCanRetryStorageFailureWithoutResurrectingMetadata(t *testing.T) {
	s, r, st := newFixture(t, 1024)
	item, err := s.Stage(context.Background(), request("retry.txt", "text/plain", []byte("hello")))
	if err != nil {
		t.Fatal(err)
	}
	st.deleteErr = errors.New("storage unavailable")
	if _, err := s.Delete(context.Background(), domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, item.ID); err == nil {
		t.Fatal("expected storage deletion failure")
	}
	st.deleteErr = nil
	r.item.Status = domain.StagedAttachmentDeleted
	if _, err := s.Delete(context.Background(), domain.Owner{Kind: domain.PrincipalAdmin, ID: "user-1"}, item.ID); err != nil {
		t.Fatal(err)
	}
	if len(st.data) != 0 {
		t.Fatal("staged bytes remained after deletion retry")
	}
}

func FuzzStageFilenameAndMIME(f *testing.F) {
	f.Add("a.txt", "text/plain", []byte("hello"))
	f.Add("../a", "image/png", []byte("plain"))
	f.Fuzz(func(t *testing.T, name, mime string, data []byte) {
		if len(data) > 2048 {
			data = data[:2048]
		}
		s, _, _ := newFixture(t, 2048)
		_, _ = s.Stage(context.Background(), request(name, mime, data))
	})
}
