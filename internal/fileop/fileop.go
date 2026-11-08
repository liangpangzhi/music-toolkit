package fileop

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileOperator 文件操作器
type FileOperator struct {
	MusicDir  string
	OutputDir string
}

func NewFileOperator(musicDir, outputDir string) *FileOperator {
	return &FileOperator{
		MusicDir:  musicDir,
		OutputDir: outputDir,
	}
}

// RenameInPlace 在原路径原地纠正后缀
func (f *FileOperator) RenameInPlace(filePath, newExt string) (bool, string, string) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false, "", "原文件不存在"
	}

	cleanExt := "." + strings.TrimPrefix(newExt, ".")
	ext := filepath.Ext(filePath)
	base := strings.TrimSuffix(filePath, ext)
	newPath := base + cleanExt

	if newPath == filePath {
		return true, filePath, "后缀已一致，无需重命名"
	}

	if _, err := os.Stat(newPath); err == nil {
		return false, "", fmt.Sprintf("目标文件已存在: %s", filepath.Base(newPath))
	}

	if err := os.Rename(filePath, newPath); err != nil {
		return false, "", fmt.Sprintf("重命名失败: %v", err)
	}
	return true, newPath, "后缀纠正成功"
}

// ProcessMismatched 复制/移动或原地修正异常文件
func (f *FileOperator) ProcessMismatched(filePath, outputDir, action string, keepStructure, fixExt bool, suggestedExt, musicRoot string) (bool, string, string) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false, "", "源文件不存在"
	}

	if action == "rename_fix" {
		return f.RenameInPlace(filePath, suggestedExt)
	}

	if action != "copy" && action != "move" {
		return true, filePath, "仅扫描模式，未改动文件"
	}

	if outputDir == "" {
		outputDir = f.OutputDir
	}
	if musicRoot == "" {
		musicRoot = f.MusicDir
	}

	absFile, _ := filepath.Abs(filePath)
	absRoot, _ := filepath.Abs(musicRoot)
	absOut, _ := filepath.Abs(outputDir)

	var targetPath string
	if keepStructure {
		rel, err := filepath.Rel(absRoot, absFile)
		if err != nil {
			rel = filepath.Base(absFile)
		} else if safe := sanitizeRel(rel); safe != "" {
			rel = safe
		} else {
			// 源文件不在音乐根目录内时退化为平铺，避免 ../ 逃逸写出目标目录
			rel = filepath.Base(absFile)
		}
		targetPath = filepath.Join(absOut, rel)
	} else {
		targetPath = filepath.Join(absOut, filepath.Base(absFile))
	}

	if fixExt && suggestedExt != "" {
		cleanExt := "." + strings.TrimPrefix(suggestedExt, ".")
		ext := filepath.Ext(targetPath)
		targetPath = strings.TrimSuffix(targetPath, ext) + cleanExt
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return false, "", fmt.Sprintf("创建目标目录失败: %v", err)
	}

	// 避免重名冲突
	if _, err := os.Stat(targetPath); err == nil && targetPath != absFile {
		ext := filepath.Ext(targetPath)
		base := strings.TrimSuffix(targetPath, ext)
		counter := 1
		for {
			candidate := fmt.Sprintf("%s_%d%s", base, counter, ext)
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				targetPath = candidate
				break
			}
			counter++
		}
	}

	if action == "copy" {
		if err := copyFile(absFile, targetPath); err != nil {
			return false, "", fmt.Sprintf("复制失败: %v", err)
		}
		return true, targetPath, "已复制到输出目录"
	} else if action == "move" {
		if err := os.Rename(absFile, targetPath); err != nil {
			// 跨盘移动 fallback
			if errCopy := copyFile(absFile, targetPath); errCopy == nil {
				os.Remove(absFile)
				return true, targetPath, "已移动到输出目录"
			}
			return false, "", fmt.Sprintf("移动失败: %v", err)
		}
		return true, targetPath, "已移动到输出目录"
	}

	return false, "", "未知操作"
}

// MoveToRecycleBin 安全移入回收站
func (f *FileOperator) MoveToRecycleBin(filePath, recycleDir string) (bool, string, string) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false, "", "文件不存在"
	}

	if recycleDir == "" {
		recycleDir = filepath.Join(f.OutputDir, "_recycle_bin")
	}

	absFile, _ := filepath.Abs(filePath)
	absMusic, _ := filepath.Abs(f.MusicDir)

	rel, err := filepath.Rel(absMusic, absFile)
	if err != nil {
		rel = filepath.Base(absFile)
	} else if safe := sanitizeRel(rel); safe != "" {
		rel = safe
	} else {
		rel = filepath.Base(absFile)
	}

	targetPath := filepath.Join(recycleDir, rel)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return false, "", fmt.Sprintf("创建回收站目录失败: %v", err)
	}

	if _, err := os.Stat(targetPath); err == nil {
		ext := filepath.Ext(targetPath)
		base := strings.TrimSuffix(targetPath, ext)
		counter := 1
		for {
			candidate := fmt.Sprintf("%s_%d%s", base, counter, ext)
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				targetPath = candidate
				break
			}
			counter++
		}
	}

	if err := os.Rename(absFile, targetPath); err != nil {
		if errCopy := copyFile(absFile, targetPath); errCopy == nil {
			os.Remove(absFile)
			return true, targetPath, "已移入回收站"
		}
		return false, "", fmt.Sprintf("移入回收站失败: %v", err)
	}
	return true, targetPath, "已移入回收站"
}

// DeletePermanently 彻底删除
func (f *FileOperator) DeletePermanently(filePath string) (bool, string, string) {
	fi, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		return true, filePath, "文件已被移除"
	}
	if err != nil {
		return false, "", fmt.Sprintf("读取文件信息失败: %v", err)
	}
	// 仅允许删除普通文件，避免误删目录导致数据灾难
	if fi.IsDir() {
		return false, "", "目标为目录，已拒绝删除"
	}
	if err := os.Remove(filePath); err != nil {
		return false, "", fmt.Sprintf("删除失败: %v", err)
	}
	return true, filePath, "文件已彻底删除"
}

// sanitizeRel 保证相对路径不会逃逸出目标根目录（防 ../ 与绝对路径注入）
func sanitizeRel(rel string) string {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.HasPrefix(rel, ".."+"/") {
		return ""
	}
	return rel
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		// 清理半成品，避免残缺文件污染曲库
		os.Remove(dst)
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err = out.Close(); err != nil {
		os.Remove(dst)
		return err
	}

	// 尽量保持与源文件一致的权限位（部分文件系统不支持，失败可忽略）
	_ = os.Chmod(dst, info.Mode().Perm())

	return nil
}
