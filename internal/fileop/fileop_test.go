package fileop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeRel(t *testing.T) {
	cases := map[string]string{
		"album/song.mp3": filepath.FromSlash("album/song.mp3"),
		"a/b/c.flac":     filepath.FromSlash("a/b/c.flac"),
		"../etc/passwd":  "",
		"../../root":     "",
		"..":             "",
		".":              "",
		"":               "",
	}
	for in, want := range cases {
		if got := sanitizeRel(in); got != want {
			t.Errorf("sanitizeRel(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestProcessMismatchedCopyStaysInsideOutput(t *testing.T) {
	base := t.TempDir()
	musicRoot := filepath.Join(base, "music")
	outDir := filepath.Join(base, "out")
	otherDir := filepath.Join(base, "other")
	for _, d := range []string{musicRoot, otherDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 源文件位于音乐根目录之外，keepStructure=true 时应退化为平铺，而不是写出 output 之外
	src := filepath.Join(otherDir, "song.mp3")
	if err := os.WriteFile(src, []byte("fake-audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	op := NewFileOperator(musicRoot, outDir)
	ok, tgt, msg := op.ProcessMismatched(src, outDir, "copy", true, false, "", musicRoot)
	if !ok {
		t.Fatalf("复制应成功，实际失败: %s", msg)
	}

	absOut, _ := filepath.Abs(outDir)
	absTgt, _ := filepath.Abs(tgt)
	if absTgt != absOut && !strings.HasPrefix(absTgt, absOut+string(os.PathSeparator)) {
		t.Fatalf("目标路径逃逸出输出目录: %s", absTgt)
	}
	if _, err := os.Stat(absTgt); err != nil {
		t.Fatalf("目标文件应已生成: %v", err)
	}
}

func TestDeletePermanentlyRejectsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "folder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	op := NewFileOperator(t.TempDir(), t.TempDir())
	ok, _, msg := op.DeletePermanently(dir)
	if ok {
		t.Fatalf("目录不应被删除（返回成功），msg=%s", msg)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("目录应仍然存在: %v", err)
	}
}

func TestCopyFileCleansUpOnFailure(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not-exist.mp3")
	dst := filepath.Join(base, "dst.mp3")

	if err := copyFile(missing, dst); err == nil {
		t.Fatalf("源文件不存在时应返回错误")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("失败后不应残留目标文件")
	}
}
