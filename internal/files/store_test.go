package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chatgo/internal/config"
	"chatgo/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	st, err := store.Open(dsn, bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	u := &store.User{Email: t.Name() + "@example.com", Name: "Owner", Role: store.RoleUser}
	if err := st.Users.Insert(context.Background(), u); err != nil {
		t.Fatalf("Users.Insert: %v", err)
	}
	dir := t.TempDir()
	return New(&config.Config{DataDir: dir}, st), st, dir
}

func testUser(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	rows, _ := st.Users.List(ctx)
	for _, u := range rows {
		return u.ID
	}
	t.Fatal("no test user")
	return ""
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return b.Bytes()
}

func fileHeader(t *testing.T, filename, declaredType string, content []byte) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	h.Set("Content-Type", declaredType)
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	part.Write(content)
	if err := mw.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}
	form, err := multipart.NewReader(&buf, mw.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("ReadForm: %v", err)
	}
	t.Cleanup(func() { form.RemoveAll() })
	return form.File["file"][0]
}

// Save must sniff the actual bytes: PNG magic named .txt with a lying
// text/plain declaration is stored AS image/png; the extension survives only
// as a cosmetic path suffix.
func TestSaveSniffsContentNotFilename(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)
	content := pngBytes(t, 600, 300)

	fh := fileHeader(t, "payload.txt", "text/plain", content)
	meta, err := svc.Save(context.Background(), uid, nil, fh, store.FileKindUpload)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if meta.Mime != "image/png" {
		t.Errorf("meta.Mime = %q, want image/png (sniff beats filename)", meta.Mime)
	}
	if meta.Name != "payload.txt" || meta.Size != int64(len(content)) {
		t.Errorf("meta = %+v", meta)
	}
	if meta.URL != "/files/"+meta.ID+"/payload.txt" {
		t.Errorf("meta.URL = %q", meta.URL)
	}

	row, err := svc.Get(context.Background(), meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := "files/" + meta.ID + ".txt"; row.Path != want {
		t.Errorf("row.Path = %q, want %q (extension is cosmetic suffix only)", row.Path, want)
	}
	rc, err := svc.Open(row)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, content) {
		t.Error("stored bytes differ from upload")
	}

	// PNG got a thumbnail: longest edge 512 (600x300 → 512x256).
	trc, err := svc.GetThumb(row)
	if err != nil {
		t.Fatalf("GetThumb: %v", err)
	}
	timg, err := jpeg.Decode(trc)
	trc.Close()
	if err != nil {
		t.Fatalf("decode thumb: %v", err)
	}
	b := timg.Bounds()
	if b.Dx() != 512 || b.Dy() != 256 {
		t.Errorf("thumb bounds = %dx%d, want 512x256", b.Dx(), b.Dy())
	}
	if _, err := os.Stat(filepath.Join(dir, "files", meta.ID+".thumb.jpg")); err != nil {
		t.Errorf("thumb not on disk: %v", err)
	}
}

func TestSaveOversizeRejected(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)

	big := pngBytes(t, 8, 8)
	big = append(big, make([]byte, MaxUploadSize)...) // > 20MB total
	fh := fileHeader(t, "big.png", "image/png", big)

	if _, err := svc.Save(context.Background(), uid, nil, fh, store.FileKindUpload); err == nil {
		t.Fatal("Save(20MB+) succeeded, want rejection")
	}
	if _, err := os.Stat(filepath.Join(dir, "files")); err == nil {
		if ents, _ := os.ReadDir(filepath.Join(dir, "files")); len(ents) > 0 {
			t.Errorf("oversize upload left files behind: %v", ents)
		}
	}
	files, total, err := st.Files.ListByUser(context.Background(), uid, "", 1, 10)
	if err != nil || total != 0 || len(files) != 0 {
		t.Errorf("expected no rows, got %d (%v)", total, err)
	}
}

func TestSaveRejectsDisallowedMime(t *testing.T) {
	svc, st, _ := newTestService(t)
	uid := testUser(t, st)

	elf := append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 600)...)
	fh := fileHeader(t, "thing.txt", "text/plain", elf)
	if _, err := svc.Save(context.Background(), uid, nil, fh, store.FileKindUpload); err == nil {
		t.Fatal("Save(ELF binary) succeeded, want content-type rejection")
	}

	fh = fileHeader(t, "kind", "image/png", pngBytes(t, 2, 2))
	if _, err := svc.Save(context.Background(), uid, nil, fh, "bogus-kind"); err == nil {
		t.Fatal("Save(kind=bogus) succeeded")
	}
}

func TestSaveTextVariants(t *testing.T) {
	svc, st, _ := newTestService(t)
	uid := testUser(t, st)
	ctx := context.Background()

	cases := []struct {
		filename, declared, body, wantMime string
	}{
		{"notes.md", "text/markdown", "# hello\nworld", "text/markdown"},
		{"data.csv", "text/csv", "a,b\n1,2", "text/csv"},
		{"obj.json", "application/json", `{"a":1}`, "application/json"},
		{"readme", "text/plain", "plain text here", "text/plain"},
	}
	for _, c := range cases {
		fh := fileHeader(t, c.filename, c.declared, []byte(c.body))
		meta, err := svc.Save(ctx, uid, nil, fh, store.FileKindUpload)
		if err != nil {
			t.Fatalf("Save(%s): %v", c.filename, err)
		}
		if meta.Mime != c.wantMime {
			t.Errorf("Save(%s) mime = %q, want %q", c.filename, meta.Mime, c.wantMime)
		}
	}
}

func TestDataURLRoundTrip(t *testing.T) {
	svc, st, _ := newTestService(t)
	uid := testUser(t, st)
	ctx := context.Background()
	content := pngBytes(t, 8, 8)

	fh := fileHeader(t, "pic.png", "image/png", content)
	meta, err := svc.Save(ctx, uid, nil, fh, store.FileKindImage)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	du, err := svc.DataURL(ctx, meta.ID)
	if err != nil {
		t.Fatalf("DataURL: %v", err)
	}
	prefix := "data:image/png;base64,"
	if !strings.HasPrefix(du, prefix) {
		t.Fatalf("DataURL prefix = %.40q", du)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(du, prefix))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if !bytes.Equal(raw, content) {
		t.Error("DataURL bytes differ from original")
	}

	// Non-images are rejected.
	fh = fileHeader(t, "doc.txt", "text/plain", []byte("hello"))
	txt, err := svc.Save(ctx, uid, nil, fh, store.FileKindUpload)
	if err != nil {
		t.Fatalf("Save txt: %v", err)
	}
	if _, err := svc.DataURL(ctx, txt.ID); err == nil {
		t.Error("DataURL on text file succeeded")
	}
}

func TestDataURLImageTooLarge(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)
	ctx := context.Background()

	id := "oversize-img"
	rel := "files/" + id + ".png"
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", id+".png"), make([]byte, maxDataURLImage+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Files.Insert(ctx, &store.File{ID: id, UserID: uid, Name: "big.png",
		Mime: "image/png", Size: maxDataURLImage + 1, Path: rel, Kind: store.FileKindImage}); err != nil {
		t.Fatalf("Files.Insert: %v", err)
	}
	if _, err := svc.DataURL(ctx, id); err == nil {
		t.Fatal("DataURL on >8MB image succeeded")
	}
}

func TestListByUserAndDelete(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)
	ctx := context.Background()

	fh := fileHeader(t, "a.png", "image/png", pngBytes(t, 40, 40))
	up, err := svc.Save(ctx, uid, nil, fh, store.FileKindUpload)
	if err != nil {
		t.Fatalf("Save upload: %v", err)
	}
	fh = fileHeader(t, "r.pdf", "application/pdf", []byte("%PDF-1.4\nstuff"))
	art, err := svc.Save(ctx, uid, nil, fh, store.FileKindArtifact)
	if err != nil {
		t.Fatalf("Save artifact: %v", err)
	}

	all, total, err := st.Files.ListByUser(ctx, uid, "", 1, 10)
	if err != nil || total != 2 || len(all) != 2 {
		t.Fatalf("list all: %d rows total=%d err=%v", len(all), total, err)
	}
	arts, total, err := st.Files.ListByUser(ctx, uid, store.FileKindArtifact, 1, 10)
	if err != nil || total != 1 || len(arts) != 1 || arts[0].ID != art.ID {
		t.Fatalf("list artifacts: %+v total=%d err=%v", arts, total, err)
	}
	uploads, total, err := st.Files.ListByUser(ctx, uid, store.FileKindUpload, 1, 10)
	if err != nil || total != 1 || len(uploads) != 1 || uploads[0].ID != up.ID {
		t.Fatalf("list uploads: %+v total=%d err=%v", uploads, total, err)
	}

	if err := svc.Delete(ctx, up.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, up.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(deleted) = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "files", up.ID+".png")); !os.IsNotExist(err) {
		t.Errorf("original bytes still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "files", up.ID+".thumb.jpg")); !os.IsNotExist(err) {
		t.Errorf("thumbnail still on disk: %v", err)
	}
	if _, err := svc.Get(ctx, art.ID); err != nil {
		t.Errorf("artifact gone after unrelated delete: %v", err)
	}
	if err := svc.Delete(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrNotFound", err)
	}
}

// GIFs are storable but never get a thumbnail (png/jpeg only, contract §files).
func TestNonPngJpegImageNoThumb(t *testing.T) {
	svc, st, dir := newTestService(t)
	uid := testUser(t, st)

	gif := append([]byte("GIF89a"), make([]byte, 500)...)
	fh := fileHeader(t, "anim.gif", "image/gif", gif)
	meta, err := svc.Save(context.Background(), uid, nil, fh, store.FileKindImage)
	if err != nil {
		t.Fatalf("Save gif: %v", err)
	}
	if meta.Mime != "image/gif" {
		t.Errorf("mime = %q, want image/gif", meta.Mime)
	}
	if _, err := os.Stat(filepath.Join(dir, "files", meta.ID+".thumb.jpg")); !os.IsNotExist(err) {
		t.Errorf("gif got a thumbnail: %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"simple.png":                      "simple.png",
		`C:\evil\..\windows.png`:          "windows.png",
		"../../etc/passwd":                "passwd",
		"..":                              "file",
		"   ":                             "file",
		strings.Repeat("x", 300) + ".png": strings.Repeat("x", 200),
		"ta\nb.txt":                       "ta_b.txt",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
