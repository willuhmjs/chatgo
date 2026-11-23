// File upload/download/gallery endpoint tests (BE-apichat owned). Prefix:
// TestAPI. Covers the DoS hardening (wire cap, disk spill), chat_id ownership
// and DTO url escaping.

package apichat

import (
	"bytes"
	"image"
	"image/png"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"testing"

	"chatgo/internal/config"
	"chatgo/internal/files"
	"chatgo/internal/store"
)

// apiServeMultipart POSTs a raw multipart body through the real mux (apiServe
// forces application/json, so uploads need their own driver).
func apiServeMultipart(t *testing.T, d *Deps, u *store.User, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, d)
	req := httptest.NewRequest("POST", "/api/files", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if u != nil {
		req = req.WithContext(withUserForTest(req.Context(), u))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// uploadBody builds a multipart upload: one "file" part (with a declared
// Content-Type like a browser sends) plus extra fields.
func uploadBody(t *testing.T, filename string, content []byte, fields map[string]string) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
		h.Set("Content-Type", mime.TypeByExtension(filepath.Ext(filename)))
		fw, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), b.Bytes()
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAPI_FileUploadHardening(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s, Files: files.New(&config.Config{DataDir: t.TempDir()}, s)}
	alice := apiTestUser(t, s, "up@x.io")
	bob := apiTestUser(t, s, "up2@x.io")
	chat := apiTestChat(t, s, alice, "upload chat")
	bobChat := apiTestChat(t, s, bob, "bob chat")

	// Basic upload still works (parse with 1MB in-RAM budget).
	ct, body := uploadBody(t, "pic.png", testPNGBytes(t), nil)
	rec := apiServeMultipart(t, d, alice, ct, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	meta := decodeJSON[files.Meta](t, rec)
	if meta.URL != "/files/"+meta.ID+"/pic.png" {
		t.Errorf("meta.URL = %q", meta.URL)
	}
	// Declared image/ + no chat → gallery kind.
	if meta.Kind != store.FileKindImage {
		t.Errorf("kind = %q, want image", meta.Kind)
	}

	// Own chat_id → kind upload, chat attached.
	ct, body = uploadBody(t, "doc.pdf", []byte("%PDF-1.4\nx"), map[string]string{"chat_id": chat.ID})
	rec = apiServeMultipart(t, d, alice, ct, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("chat upload: %d %s", rec.Code, rec.Body.String())
	}
	if m := decodeJSON[files.Meta](t, rec); m.Kind != store.FileKindUpload {
		t.Errorf("chat upload kind = %q", m.Kind)
	}

	// Foreign chat_id → 404, nothing stored.
	before := countUserFiles(t, s, alice)
	ct, body = uploadBody(t, "doc.pdf", []byte("%PDF-1.4\nx"), map[string]string{"chat_id": bobChat.ID})
	if rec := apiServeMultipart(t, d, alice, ct, body); rec.Code != http.StatusNotFound {
		t.Errorf("foreign chat_id: %d, want 404", rec.Code)
	}
	// Unknown chat_id → 404 too.
	ct, body = uploadBody(t, "doc.pdf", []byte("%PDF-1.4\nx"), map[string]string{"chat_id": "ghost"})
	if rec := apiServeMultipart(t, d, alice, ct, body); rec.Code != http.StatusNotFound {
		t.Errorf("unknown chat_id: %d, want 404", rec.Code)
	}
	if got := countUserFiles(t, s, alice); got != before {
		t.Errorf("rejected chat uploads stored %d files", got-before)
	}

	// Wire body above MaxUploadSize+1MB slack → 400 via MaxBytesReader
	// (CapJSONBody skips multipart, so this cap is the only body bound).
	huge := make([]byte, files.MaxUploadSize+(1<<20)+64<<10)
	copy(huge, []byte("%PDF-1.4\n"))
	ct, body = uploadBody(t, "huge.pdf", huge, nil)
	if len(body) <= int(files.MaxUploadSize)+(1<<20) {
		t.Fatalf("test body %d bytes is under the wire cap", len(body))
	}
	if rec := apiServeMultipart(t, d, alice, ct, body); rec.Code != http.StatusBadRequest {
		t.Errorf("over-cap wire body: %d, want 400", rec.Code)
	}
}

// DTO/gallery URLs must be path-escaped (raw names with spaces/unicode break
// the reference and can smuggle path segments).
func TestAPI_FileListURLEscaped(t *testing.T) {
	s := apiTestStore(t)
	d := &Deps{Store: s, Files: files.New(&config.Config{DataDir: t.TempDir()}, s)}
	alice := apiTestUser(t, s, "esc@x.io")

	f := &store.File{ID: "esc-1", UserID: alice.ID, Name: "two words.png",
		Mime: "image/png", Size: 4, Path: "files/esc-1.png", Kind: store.FileKindImage}
	if err := s.Files.Insert(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	rec := apiServe(t, d, alice, "GET", "/api/files", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	page := decodeJSON[struct {
		Items []fileDTO `json:"items"`
	}](t, rec)
	if len(page.Items) != 1 {
		t.Fatalf("items: %+v", page.Items)
	}
	if want := "/files/esc-1/two%20words.png"; page.Items[0].URL != want {
		t.Errorf("url = %q, want %q", page.Items[0].URL, want)
	}
}

func countUserFiles(t *testing.T, s *store.Store, u *store.User) int {
	t.Helper()
	_, total, err := s.Files.ListByUser(t.Context(), u.ID, "", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return total
}
