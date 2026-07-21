package warehouse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/nucleus/pkg/storage"
)

func TestIsStorageNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed ErrNotFound", storage.ErrNotFound("products/1/datasheet"), true},
		{"wrapped ErrNotFound", fmt.Errorf("get: %w", storage.ErrNotFound("k")), true},
		// The s3 provider workaround: the S3 client's message for a missing
		// object ("The specified key does not exist.") is not mapped to
		// storage.ErrNotFound upstream, so the text check must catch it.
		{"raw S3 missing-key text", errors.New("The specified key does not exist."), true},
		{"unrelated error", errors.New("connection refused"), false},
		{"unrelated wrapped", fmt.Errorf("put: %w", errors.New("access denied")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStorageNotFound(tc.err); got != tc.want {
				t.Fatalf("isStorageNotFound(%v) = %v (want %v)", tc.err, got, tc.want)
			}
		})
	}
}

func TestDatasheetKey(t *testing.T) {
	if got := datasheetKey(42); got != "products/42/datasheet" {
		t.Fatalf("datasheetKey(42) = %q", got)
	}
}

// fakeStore is a minimal storage.Store for handler unit tests: Get serves a
// fixed object for one key and storage.ErrNotFound for everything else.
type fakeStore struct {
	key         string
	content     string
	contentType string
}

func (f *fakeStore) Put(ctx context.Context, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, errors.New("fakeStore: Put not supported")
}

func (f *fakeStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	if key != f.key {
		return nil, storage.ObjectInfo{}, storage.ErrNotFound(key)
	}
	info := storage.ObjectInfo{Key: key, Size: int64(len(f.content)), ContentType: f.contentType}
	return io.NopCloser(strings.NewReader(f.content)), info, nil
}

func (f *fakeStore) Delete(ctx context.Context, key string) error { return nil }
func (f *fakeStore) Exists(ctx context.Context, key string) (bool, error) {
	return key == f.key, nil
}
func (f *fakeStore) List(ctx context.Context, opts storage.ListOptions) (storage.ListResult, error) {
	return storage.ListResult{}, nil
}
func (f *fakeStore) PublicURL(ctx context.Context, key string, opts storage.URLConfig) (string, error) {
	return "", nil
}
func (f *fakeStore) SignedURL(ctx context.Context, key string, expires time.Duration, opts storage.URLConfig) (string, error) {
	return "", nil
}
func (f *fakeStore) Copy(ctx context.Context, srcKey, dstKey string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, errors.New("fakeStore: Copy not supported")
}
func (f *fakeStore) Close() error { return nil }

// TestGetDatasheetHeaders pins the stored-XSS defence on the datasheet
// download: the handler must send X-Content-Type-Options: nosniff and offer
// the body as an attachment, never inline.
func TestGetDatasheetHeaders(t *testing.T) {
	m := &module{store: &fakeStore{
		key:         datasheetKey(7),
		content:     "<script>alert(1)</script>",
		contentType: "text/html",
	}}

	rec := httptest.NewRecorder()
	c := testContext(rec, httptest.NewRequest(http.MethodGet, "/api/products/7/datasheet", nil), "7")
	if err := m.getDatasheet(c); err != nil {
		t.Fatalf("getDatasheet: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("getDatasheet: status %d", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q (want nosniff)", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("Content-Disposition = %q (want attachment)", got)
	}
	if got := rec.Body.String(); got != "<script>alert(1)</script>" {
		t.Fatalf("body = %q", got)
	}

	// Missing object still maps to 404 through isStorageNotFound.
	rec = httptest.NewRecorder()
	c = testContext(rec, httptest.NewRequest(http.MethodGet, "/api/products/9/datasheet", nil), "9")
	if err := m.getDatasheet(c); err != nil {
		t.Fatalf("getDatasheet (missing): %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("getDatasheet (missing): status %d (want 404)", rec.Code)
	}
}
