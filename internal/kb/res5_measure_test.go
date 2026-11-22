// RES-5 ingest-RAM measurement harness (last flagged residual from RES-4:
// embedTexts accumulated ALL vectors for a file — ~22 MiB of 1536-dim float32
// per 5 MiB worst-case upload — until insertFileChunks committed them at the
// end; peak ~83 MiB live heap / 221 MiB RSS on the RES-4 tree).
//
// Skipped unless KB_INGEST_MEASURE=1. Production SHAPE (disk-backed SQLite +
// streaming multipart client), real kb surface on 127.0.0.1:8109 (override
// KB_RES5_ADDR) behind a fixed-port 1536-dim embeddings stub on
// 127.0.0.1:9119 (override KB_RES5_STUB). Reports baseline and S1 peak live
// heap + peak RSS (getrusage):
//
//	S1  one 5 MiB 1-word/line upload (max chunk count = max vectors/file)
//
// This file is deliberately BLACK-BOX (HTTP + chunker-free): it compiles and
// runs identically on the pre-fix tree and the pipelined tree, so the only
// variable is the ingest code. Run pre/post identically, e.g.
//
//	go test -c ./internal/kb -o /tmp/res5.test
//	KB_INGEST_MEASURE=1 GOMEMLIMIT=100MiB /tmp/res5.test -test.run TestRes5IngestRAM -test.v
//
// (Run pre and post SEQUENTIALLY — both bind 8109/9119.)
package kb

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/store"
)

func res5EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// res5Serve starts an http server on a fixed loopback port and returns a
// cleanup. Plain net/http (no httptest) so the port is exactly the measured
// one and stays off the ports other RES harnesses own.
func res5Serve(t *testing.T, addr string, h http.Handler) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func res5StubEmbed(w http.ResponseWriter, r *http.Request, dims int, latency time.Duration, batches *atomic.Int64) {
	batches.Add(1)
	time.Sleep(latency) // hold the slot like a real provider round-trip
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	var in struct {
		Input []string `json:"input"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	data := make([]json.RawMessage, 0, len(in.Input))
	for i := range in.Input {
		v := make([]float32, dims)
		for j := range v {
			v[j] = float32((i+j)%7) / 100
		}
		// Hand-build the item so the stub's own encoding cost matches a real
		// provider's JSON body.
		b, err := json.Marshal(map[string]any{"index": i, "embedding": v})
		if err != nil {
			http.Error(w, "encode", http.StatusInternalServerError)
			return
		}
		data = append(data, b)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// res5Sample runs a HeapAlloc sampler for the duration of fn.
func res5Sample(fn func() error) (uint64, error) {
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.ReadMemStats(&ms)
			if p := peak.Load(); ms.HeapAlloc > p {
				peak.CompareAndSwap(p, ms.HeapAlloc)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	err := fn()
	close(stop)
	wg.Wait()
	return peak.Load(), err
}

func res5MaxRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	return ru.Maxrss // bytes on darwin
}

func TestRes5IngestRAM(t *testing.T) {
	if os.Getenv("KB_INGEST_MEASURE") == "" {
		t.Skip("set KB_INGEST_MEASURE=1 (scratch RSS harness, binds 127.0.0.1:8109 + :9119)")
	}
	dims := 1536
	if v := os.Getenv("KB_INGEST_DIMS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d <= 0 {
			t.Fatalf("KB_INGEST_DIMS=%q", v)
		}
		dims = d
	}
	appAddr := res5EnvOr("KB_RES5_ADDR", "127.0.0.1:8109")
	stubAddr := res5EnvOr("KB_RES5_STUB", "127.0.0.1:9119")
	latency := 25 * time.Millisecond
	var batches atomic.Int64

	res5Serve(t, stubAddr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res5StubEmbed(w, r, dims, latency, &batches)
	}))

	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "res5.db"), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	secret := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cfg := &config.Config{SessionSecret: secret, AllowLoopback: true, DataDir: t.TempDir()}
	a, err := auth.New(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Providers.Insert(ctx, &store.Provider{
		ID: "p-embed", Name: "stub", BaseURL: "http://" + stubAddr + "/v1", APIKey: "sk-stub", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Models.ReplaceForProvider(ctx, "p-embed", []store.ModelRow{
		{ModelID: "text-embedding-3-small", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &Deps{Cfg: cfg, Store: st, Auth: a})
	res5Serve(t, appAddr, a.Require(mux))

	// Seed one user + collection.
	u := &store.User{Email: "res5@x.dev", Name: "res5", Role: store.RoleUser}
	if err := st.Users.Insert(ctx, u); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	tok := "tok-res5"
	if err := st.Sessions.Create(ctx, &store.Session{
		Token: tok, UserID: u.ID, CreatedAt: now, ExpiresAt: now + 3600, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	var collID string
	collID = fmt.Sprintf("coll-res5-%d", now)
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO kb_collections (id, user_id, name, created_at) VALUES (?1, ?2, ?3, ?4)`,
		collID, u.ID, "res5", now); err != nil {
		t.Fatal(err)
	}

	base := "http://" + appAddr
	upload := func(body []byte) int {
		// Streaming multipart client (browser-like): client side holds no
		// whole-body copy beyond the fixture slice itself.
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		ct := mw.FormDataContentType()
		go func() {
			fw, _ := mw.CreateFormFile("file", "res5.txt")
			_, _ = fw.Write(body)
			_ = mw.Close()
			_ = pw.Close()
		}()
		req, _ := http.NewRequest("POST", base+"/api/kb/"+collID+"/files", pr)
		req.Header.Set("Content-Type", ct)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("upload: transport error %v", err)
			return 0
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	// Warm-up upload so baseline RSS reflects a live server, not the loader.
	if st := upload([]byte("golang warm-up notes\n")); st != 201 {
		t.Fatalf("warm-up upload: %d", st)
	}
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	baseHeap := ms.HeapAlloc
	baseRSS := res5MaxRSS()

	// ---- S1: single 5 MiB 1-word/line upload (RES-4's worst case) --------
	const n = 5 << 20
	body := bytes.Repeat([]byte("w\n"), n/2)
	if len(body) != n {
		t.Fatalf("fixture %d bytes, want %d", len(body), n)
	}
	batches.Store(0)
	peak, err := res5Sample(func() error {
		if st := upload(body); st != 201 {
			return fmt.Errorf("S1 upload: %d", st)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("S1: %v", err)
	}
	fmt.Printf("RES5 baseline: live_heap=%.1f MiB rss=%.1f MiB memlimit=%q dims=%d\n",
		float64(baseHeap)/(1<<20), float64(baseRSS)/(1<<20), os.Getenv("GOMEMLIMIT"), dims)
	fmt.Printf("RES5 S1 (1×5MiB 1-word/line): peak_live_heap=%.1f MiB (delta=%.1f) peak_rss=%.1f MiB embed_batches=%d\n",
		float64(peak)/(1<<20), float64(peak-baseHeap)/(1<<20),
		float64(res5MaxRSS())/(1<<20), batches.Load())
	var gc runtime.MemStats
	runtime.ReadMemStats(&gc)
	fmt.Printf("RES5 gc: num_gc=%d forced=%d\n", gc.NumGC, gc.NumForcedGC)
}
