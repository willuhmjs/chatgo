// RES-4 ingest-RAM measurement harness (MEDIUM finding: one 5 MiB upload of
// 1-word/lines — the max-chunk-count worst case — carried a ~32 MiB live set,
// and the 4×(8×5 MiB) lens storm pinned the GC heap at 89/100 MiB).
//
// Skipped unless KB_INGEST_MEASURE=1. Serves the real kb surface on
// 127.0.0.1:8108 behind a fake 1536-dim embeddings stub (matching
// text-embedding-3-small vector size) and reports sampled peak live heap and
// peak RSS (getrusage) for:
//
//	S1     one 5 MiB 1-word/line upload (chunker worst case)
//	S2     S2-lite: 8 such uploads fired concurrently from 8 users
//
// Run pre/post fix identically, e.g.
//
//	go test -c ./internal/kb -o /tmp/kb.test
//	KB_INGEST_MEASURE=1 GOMEMLIMIT=100MiB /tmp/kb.test -test.run TestRes4IngestRAM -test.v
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
	"net/http/httptest"
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

var (
	mres4Dims    = 1536 // text-embedding-3-small width; override: KB_INGEST_DIMS
	mres4Latency = 25 * time.Millisecond
)

type mres4Env struct {
	t        *testing.T
	srv      *http.Server
	base     string
	tokens   []string
	colls    []string
	stream   bool // stream the multipart body via io.Pipe (client side uses ~0 RAM)
	batches  atomic.Int64
	inflight atomic.Int64
	peakConc atomic.Int64
}

func mres4StubEmbed(w http.ResponseWriter, r *http.Request, inflight, peakConc *atomic.Int64) {
	cur := inflight.Add(1)
	for { // CAS-max
		old := peakConc.Load()
		if cur <= old || peakConc.CompareAndSwap(old, cur) {
			break
		}
	}
	defer inflight.Add(-1)
	time.Sleep(mres4Latency) // hold the slot like a real provider round-trip

	raw, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	var in struct {
		Input []string `json:"input"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	data := make([]map[string]any, 0, len(in.Input))
	for i := range in.Input {
		v := make([]float32, mres4Dims)
		for j := range v {
			v[j] = float32(i+j%7) / 100
		}
		data = append(data, map[string]any{"index": i, "embedding": v})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func mres4MaxRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	return ru.Maxrss // bytes on darwin
}

// mres4Sample runs a HeapAlloc sampler for the duration of fn and returns the
// peak live heap observed plus fn's error (if any).
func mres4Sample(fn func() error) (uint64, error) {
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

func mres4Run(t *testing.T, addr string) *mres4Env { return mres4RunMode(t, addr, false) }

// mres4RunMode builds the harness. prod=true switches to the production SHAPE:
// disk-backed SQLite (page cache instead of a whole-DB-in-RAM page store) and
// a streaming multipart client (a browser uploads incrementally; buffering
// 5 MiB per concurrent upload client-side would swamp the measurement).
func mres4RunMode(t *testing.T, addr string, prod bool) *mres4Env {
	t.Helper()
	ctx := context.Background()
	e := &mres4Env{t: t, stream: prod}
	if v := os.Getenv("KB_INGEST_DIMS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d <= 0 {
			t.Fatalf("KB_INGEST_DIMS=%q", v)
		}
		mres4Dims = d
	}

	stub := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.batches.Add(1)
		mres4StubEmbed(w, r, &e.inflight, &e.peakConc)
	}))
	stub.Start()
	t.Cleanup(stub.Close)

	name := "file:mem_res4_" + fmt.Sprint(time.Now().UnixNano()) + "?mode=memory&cache=shared"
	if prod {
		name = filepath.Join(t.TempDir(), "res4.db")
	}
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
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
		ID: "p-embed", Name: "stub", BaseURL: stub.URL + "/v1", APIKey: "sk-stub", Enabled: true,
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

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	e.srv = &http.Server{Handler: a.Require(mux)}
	go func() { _ = e.srv.Serve(ln) }()
	t.Cleanup(func() { _ = e.srv.Close() })
	e.base = "http://" + ln.Addr().String()

	// Seed users + collections (one each so the 5/min per-user limiter never
	// interferes with S2's 8 concurrent uploads).
	for i := 0; i < 8; i++ {
		email := fmt.Sprintf("res4-%d@x.dev", i)
		u := &store.User{Email: email, Name: email, Role: store.RoleUser}
		if err := st.Users.Insert(ctx, u); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		tok := fmt.Sprintf("tok-res4-%d", i)
		if err := st.Sessions.Create(ctx, &store.Session{
			Token: tok, UserID: u.ID, CreatedAt: now, ExpiresAt: now + 3600, LastSeenAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		c := &collection{ID: fmt.Sprintf("coll-res4-%d", i), UserID: u.ID, Name: email, CreatedAt: now}
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO kb_collections (id, user_id, name, created_at) VALUES (?1, ?2, ?3, ?4)`,
			c.ID, c.UserID, c.Name, c.CreatedAt); err != nil {
			t.Fatal(err)
		}
		e.tokens = append(e.tokens, tok)
		e.colls = append(e.colls, c.ID)
	}
	return e
}

// mres4Upload POSTs body as file "res4.txt" with the given session token.
// In stream mode the multipart body is piped (chunked transfer) so the
// client side holds no 5 MiB buffer copy.
func (m *mres4Env) upload(userIdx int, body []byte) int {
	var rdr io.Reader
	var ct string
	if m.stream {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		ct = mw.FormDataContentType()
		go func() {
			fw, _ := mw.CreateFormFile("file", "res4.txt")
			_, _ = fw.Write(body)
			_ = mw.Close()
			_ = pw.Close()
		}()
		rdr = pr
	} else {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "res4.txt")
		_, _ = fw.Write(body)
		_ = mw.Close()
		ct = mw.FormDataContentType()
		rdr = &buf
	}
	req, _ := http.NewRequest("POST", m.base+"/api/kb/"+m.colls[userIdx]+"/files", rdr)
	req.Header.Set("Content-Type", ct)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: m.tokens[userIdx]})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		m.t.Logf("upload user %d: transport error %v", userIdx, err)
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// mres4Sink keeps measured outputs reachable past the sampler window.
var mres4Sink []string

// mres4LivePeak runs fn while sampling live heap every ms and returns the
// peak HeapAlloc observed minus the pre-run live heap (i.e. fn's own live
// working set, GC included).
func mres4LivePeak(fn func()) uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	peak, _ := mres4Sample(func() error { fn(); return nil })
	if peak < base {
		return 0
	}
	return peak - base
}

func TestRes4IngestRAM(t *testing.T) {
	mode := os.Getenv("KB_INGEST_MEASURE")
	if mode == "" {
		t.Skip("set KB_INGEST_MEASURE=1|prod (scratch RSS harness, binds 127.0.0.1:8108)")
	}
	e := mres4RunMode(t, "127.0.0.1:8108", mode == "prod")

	// Worst-case payload: exactly 5 MiB of 1-char words, one per line
	// (max word count ⇒ max span-table + max chunk-count pressure).
	const n = 5 << 20
	body := bytes.Repeat([]byte("w\n"), n/2)
	if len(body) != n {
		t.Fatalf("fixture %d bytes, want %d", len(body), n)
	}

	// ---- S0: chunker-only live set, streaming vs old whole-file ----------
	// (harness-independent A/B of the RES-4 fix itself: old path = whole-file
	// string copy + whole-file span table; new = single scan, bounded spans)
	oldLive := mres4LivePeak(func() { mres4Sink = refChunkText(string(body)) })
	mres4Sink = nil
	newLive := mres4LivePeak(func() { mres4Sink = chunkBytes(body) })
	mres4Sink = nil
	fmt.Printf("RES4 S0 chunker-only (5 MiB 1-word/line): live old=%.1f MiB new=%.1f MiB\n",
		float64(oldLive)/(1<<20), float64(newLive)/(1<<20))

	// Warm-up upload so baseline RSS reflects a live server, not the loader.
	warm := []byte("golang warm-up notes\n")
	if st := e.upload(0, warm); st != http.StatusCreated {
		t.Fatalf("warm-up upload: %d", st)
	}
	runtime.GC()
	baseHeap := func() uint64 {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}()
	baseRSS := mres4MaxRSS()

	fmt.Printf("RES4 baseline (mode=%s): heap_live=%.1f MiB rss=%.1f MiB memlimit=%s\n",
		mode, float64(baseHeap)/(1<<20), float64(baseRSS)/(1<<20), os.Getenv("GOMEMLIMIT"))

	// ---- S1: single 5 MiB 1-word/line upload ----------------------------
	e.batches.Store(0)
	e.peakConc.Store(0)
	peak, err := mres4Sample(func() error {
		if st := e.upload(1, body); st != http.StatusCreated {
			return fmt.Errorf("S1 upload: %d", st)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("S1: %v", err)
	}
	fmt.Printf("RES4 S1 (1×5MiB 1-word/line): peak_live_heap=%.1f MiB peak_rss=%.1f MiB embed_batches=%d\n",
		float64(peak)/(1<<20), float64(mres4MaxRSS())/(1<<20), e.batches.Load())

	// ---- S2-lite: 8 concurrent uploads (lens-storm shape, 1 wave) -------
	e.peakConc.Store(0)
	peak, err = mres4Sample(func() error {
		var wg sync.WaitGroup
		codes := make([]int, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) { defer wg.Done(); codes[i] = e.upload(i, body) }(i)
		}
		wg.Wait()
		ok, busy := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				ok++
			case http.StatusTooManyRequests:
				busy++
			default:
				return fmt.Errorf("S2 unexpected status %d", c)
			}
		}
		fmt.Printf("RES4 S2-lite (8 concurrent×5MiB): admitted=%d 429=%d peak_concurrent_embed_calls=%d\n",
			ok, busy, e.peakConc.Load())
		return nil
	})
	if err != nil {
		t.Fatalf("S2: %v", err)
	}
	fmt.Printf("RES4 S2-lite: peak_live_heap=%.1f MiB peak_rss=%.1f MiB\n",
		float64(peak)/(1<<20), float64(mres4MaxRSS())/(1<<20))

	var gc runtime.MemStats
	runtime.ReadMemStats(&gc)
	fmt.Printf("RES4 gc: num_gc=%d forced=%d gomemlimit=%q\n", gc.NumGC, gc.NumForcedGC, os.Getenv("GOMEMLIMIT"))
}
