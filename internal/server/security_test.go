package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"music-toolkit/internal/config"
)

func newTestConfig(root, out string) *config.Config {
	return &config.Config{
		MusicDir:   root,
		MusicDirs:  []string{root},
		OutputDir:  out,
		DBPath:     filepath.Join(out, "test.db"),
		FFmpegPath: "ffmpeg",
		Port:       6826,
		MaxWorkers: 2,
	}
}

func TestPathUnderRoots(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()

	inside := filepath.Join(root, "album", "song.flac")
	outside := filepath.Join(other, "secret.txt")
	escape := filepath.Join(root, "..", filepath.Base(other), "secret.txt")

	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"库内文件", inside, true},
		{"库根目录本身", root, true},
		{"库外文件", outside, false},
		{"相对路径逃逸", escape, false},
		{"空路径", "", false},
	}

	roots := []string{root}
	for _, c := range cases {
		if got := pathUnderRoots(c.target, roots); got != c.want {
			t.Errorf("%s: pathUnderRoots(%q) = %v, 期望 %v", c.name, c.target, got, c.want)
		}
	}
}

func TestRequireUnderRootsRejectsOutside(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "etc", "passwd")

	s := &Server{}
	s.cfg = newTestConfig(root, filepath.Join(root, "_out"))

	rec := httptest.NewRecorder()
	if s.requireUnderRoots(rec, outside) {
		t.Fatalf("库外路径应被拒绝")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	insideFile := filepath.Join(root, "a.mp3")
	if !s.requireUnderRoots(rec, insideFile) {
		t.Fatalf("库内路径应被放行")
	}
}

func TestIsSameOrigin(t *testing.T) {
	cases := []struct {
		origin string
		host   string
		want   bool
	}{
		{"http://localhost:6826", "localhost:6826", true},
		{"http://nas.local:6826", "nas.local:6826", true},
		{"http://127.0.0.1:6826", "nas.local:6826", true},
		{"https://evil.example.com", "nas.local:6826", false},
		{"", "nas.local:6826", false},
		{"not-a-url", "nas.local:6826", false},
	}
	for _, c := range cases {
		if got := isSameOrigin(c.origin, c.host); got != c.want {
			t.Errorf("isSameOrigin(%q, %q) = %v, 期望 %v", c.origin, c.host, got, c.want)
		}
	}
}

func TestCorsMiddlewareBlocksForeignOriginPreflight(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 第三方站点预检请求应被拒绝
	req := httptest.NewRequest(http.MethodOptions, "/api/format/action/batch", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站预检应返回 403，实际 %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("跨站请求不应下发 CORS 放行头")
	}

	// 同源请求正常放行
	req = httptest.NewRequest(http.MethodOptions, "/api/format/action/batch", nil)
	req.Header.Set("Origin", "http://localhost:6826")
	req.Host = "localhost:6826"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("同源预检应返回 204，实际 %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:6826" {
		t.Fatalf("同源预检应回显来源，实际 %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestDirStatsCacheReuse(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{}
	s.cfg = newTestConfig(root, filepath.Join(root, "_out"))
	s.dirStatsCache = make(map[string]dirStatsCacheEntry)

	first := s.scanMultiDirStatsCached([]string{root}, s.cfg.OutputDir)
	second := s.scanMultiDirStatsCached([]string{root}, s.cfg.OutputDir)
	if first.TotalFiles != second.TotalFiles {
		t.Fatalf("缓存前后结果不一致: %d vs %d", first.TotalFiles, second.TotalFiles)
	}

	// 新增文件后，缓存未过期前仍返回旧值，失效后应重新统计
	if err := os.WriteFile(filepath.Join(root, "b.mp3"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	cached := s.scanMultiDirStatsCached([]string{root}, s.cfg.OutputDir)
	if cached.TotalFiles != first.TotalFiles {
		t.Fatalf("缓存期内应返回旧统计，实际 %d，期望 %d", cached.TotalFiles, first.TotalFiles)
	}

	s.invalidateDirStatsCache()
	fresh := s.scanMultiDirStatsCached([]string{root}, s.cfg.OutputDir)
	if fresh.TotalFiles <= first.TotalFiles {
		t.Fatalf("缓存失效后应重新统计到新增文件，实际 %d", fresh.TotalFiles)
	}
}
