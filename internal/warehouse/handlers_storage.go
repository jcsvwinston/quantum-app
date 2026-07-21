package warehouse

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/storage"
	"github.com/jcsvwinston/quark"
)

// maxDatasheetBytes caps datasheet uploads.
const maxDatasheetBytes = 10 << 20 // 10 MiB

func datasheetKey(productID int64) string {
	return fmt.Sprintf("products/%d/datasheet", productID)
}

// isStorageNotFound reports whether err means "object does not exist".
//
// It checks storage.ErrNotFound first, plus a workaround for the s3 provider
// in every nucleus release up to and including the pinned v1.4.0: its
// not-found detection matches on the error TEXT ("NoSuchKey"/"not found"),
// but the S3 client's error string for a missing object is "The specified
// key does not exist." (the NoSuchKey code travels in the typed response,
// not the message), so Get on a missing key surfaces a raw error instead of
// storage.ErrNotFound. Upstream already classifies by the SDK's typed error
// on nucleus main (post-v1.4.0, not yet in a tagged release); drop the
// string check when the pinned nucleus includes that fix.
func isStorageNotFound(err error) bool {
	var nf storage.ErrNotFound
	if errors.As(err, &nf) {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "does not exist")
}

// putDatasheet stores the request body as the product's datasheet in the
// configured object store (S3/MinIO in this deployment).
func (m *module) putDatasheet(c *nucleus.Context) error {
	if !m.requireUser(c) {
		return nil
	}
	id, ok := parseID(c)
	if !ok {
		return nil
	}
	ctx := c.Request.Context()
	if _, err := quark.For[Product](ctx, m.bridgedPG).Find(id); err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "product not found"})
	}

	key := datasheetKey(id)
	body := http.MaxBytesReader(c.Writer, c.Request.Body, maxDatasheetBytes)
	info, err := m.store.Put(ctx, key, body, storage.PutOptions{
		ContentType: c.Request.Header.Get("Content-Type"),
	})
	if err != nil {
		return err
	}
	if _, err := quark.For[Product](ctx, m.bridgedPG).
		Where("id", "=", id).
		UpdateMap(map[string]any{"attachment_key": info.Key}); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"key":          info.Key,
		"size":         info.Size,
		"content_type": info.ContentType,
	})
}

// getDatasheet streams the stored object back.
func (m *module) getDatasheet(c *nucleus.Context) error {
	id, ok := parseID(c)
	if !ok {
		return nil
	}
	ctx := c.Request.Context()
	rc, info, err := m.store.Get(ctx, datasheetKey(id))
	if err != nil {
		if isStorageNotFound(err) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "no datasheet"})
		}
		return err
	}
	defer rc.Close()

	if info.ContentType != "" {
		c.Writer.Header().Set("Content-Type", info.ContentType)
	}
	c.Writer.WriteHeader(http.StatusOK)
	_, err = io.Copy(c.Writer, rc)
	return err
}

// deleteDatasheet removes the object and clears the product's attachment key.
func (m *module) deleteDatasheet(c *nucleus.Context) error {
	if !m.requireUser(c) {
		return nil
	}
	id, ok := parseID(c)
	if !ok {
		return nil
	}
	ctx := c.Request.Context()
	if err := m.store.Delete(ctx, datasheetKey(id)); err != nil {
		return err
	}
	if _, err := quark.For[Product](ctx, m.bridgedPG).
		Where("id", "=", id).
		UpdateMap(map[string]any{"attachment_key": ""}); err != nil {
		return err
	}
	return c.NoContent()
}
