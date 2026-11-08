package lossless

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LosslessGrade 无损评级
type LosslessGrade string

const (
	GradeTrueHiRes   LosslessGrade = "true_hires"       // 🏆 真 Hi-Res 高解析无损 (96k/192k)
	GradeTrueCD      LosslessGrade = "true_lossless"    // 💎 真无损 (CD 44.1k/48k 标准无损)
	GradeSuspectFake LosslessGrade = "fake_320k"        // ⚠️ 假无损 (疑似 320k MP3/AAC 转压)
	GradeBadFake     LosslessGrade = "fake_low_bitrate" // 🚫 劣质假无损 (疑似 128k~192k MP3 转压)
	GradeUnknown     LosslessGrade = "unknown"          // 未知/检测失败
)

// LosslessResult 检测结果
type LosslessResult struct {
	FilePath       string        `json:"file_path"`
	FileName       string        `json:"file_name"`
	FileSize       int64         `json:"file_size"`
	Format         string        `json:"format"`
	SampleRate     int           `json:"sample_rate"`
	Bitrate        int           `json:"bitrate"`
	Duration       float64       `json:"duration"`
	Grade          LosslessGrade `json:"grade"`
	GradeText      string        `json:"grade_text"`
	CutoffFreqHz   int           `json:"cutoff_freq_hz"`   // 探测到的高频截止频率
	HighFreqEnergy float64       `json:"high_freq_energy"` // 高频能量衰减 (dB)
	Confidence     int           `json:"confidence"`       // 置信度 (0-100)
	Details        string        `json:"details"`
}

var rmsRe = regexp.MustCompile(`RMS level dB:\s+(-?inf|[-+]?[\d.]+)`)
var peakRe = regexp.MustCompile(`Peak level dB:\s+([-\d.]+)`)

// AnalyzeLossless 分析单首无损音乐的频谱真实度
func AnalyzeLossless(ctx context.Context, ffmpegPath, filePath string, sampleRate int) LosslessResult {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	// 采样策略（P1-1 修复）：固定 25 秒分析窗口，并按总时长相对定位起点（约 35% 处），
	// 让 5 秒试听片段与 60 秒整曲落在相同的相对位置，避免同一素材仅因采样点不同而得出相反结论。
	startSec := 0
	windowSec := sampleWindowSec
	if dur, err := probeDurationSec(ctx, ffmpegPath, filePath); err == nil && dur > 0 {
		startSec = chooseSampleStart(dur, windowSec)
	}

	fullRMS := measureBandEnergy(ctx, ffmpegPath, filePath, "", startSec, windowSec)
	if fullRMS == detectFailSentinel || math.IsInf(fullRMS, 0) || math.IsNaN(fullRMS) {
		return LosslessResult{
			FilePath:   filePath,
			Grade:      GradeUnknown,
			GradeText:  "检测失败",
			Details:    "无法解码音频样本进行频谱分析",
			Confidence: 0,
		}
	}

	// 各频段能量统一归一化（P1-2 修复）：-inf / 无信号 / 解析失败一律收敛到 noSignalDBCap，
	// 杜绝 -999 哨兵值参与减法运算后输出 977.9dB 这类违反物理常识的数值。
	profile := bandProfile{
		Full: fullRMS,
		B16:  normalizeBandDB(measureBandEnergy(ctx, ffmpegPath, filePath, "highpass=f=16000", startSec, windowSec)),
		B20:  normalizeBandDB(measureBandEnergy(ctx, ffmpegPath, filePath, "highpass=f=20000", startSec, windowSec)),
		B22:  normalizeBandDB(measureBandEnergy(ctx, ffmpegPath, filePath, "highpass=f=22000", startSec, windowSec)),
	}

	v := classifyBands(profile, sampleRate)
	return LosslessResult{
		FilePath:       filePath,
		Grade:          v.Grade,
		GradeText:      v.GradeText,
		CutoffFreqHz:   v.CutoffHz,
		HighFreqEnergy: v.HighFreqEnergy,
		Confidence:     v.Confidence,
		Details:        v.Details,
	}
}

// 判定相关常量（阈值基于真无损/128k/192k/320k/AAC 频谱实测定标）
const (
	// sampleWindowSec 固定分析窗口长度（秒）
	sampleWindowSec = 25
	// noSignalDBCap 频段无信号（-inf / 完全静音）时的归一地板值
	noSignalDBCap = -120.0
	// detectFailSentinel 解码/解析失败标记，仅用于流程判断，绝不进入展示数值
	detectFailSentinel = -999.0
	// cutoff16kThresholdDB 16kHz 以上相对衰减超过该值判定为 16k 硬截断（128k~192k）
	cutoff16kThresholdDB = 45.0
	// cutoff22kThresholdDB 22kHz 以上相对衰减超过该值判定为有损高频刀切
	cutoff22kThresholdDB = 70.0
	// hiresDiff22kThresholdDB Hi-Res 判定允许的 22kHz 以上相对衰减上限
	hiresDiff22kThresholdDB = 35.0
	// displayCeilDB 展示数值上限钳制，避免 UI 出现超出物理范围的 dB 值
	displayCeilDB = 100.0
)

// chooseSampleStart 依据总时长与窗口长度计算采样起点，保证不同时长文件采样相对位置一致
func chooseSampleStart(duration float64, windowSec int) int {
	if windowSec <= 0 || duration <= float64(windowSec) {
		return 0
	}
	start := int(math.Round(duration * 0.35))
	if float64(start)+float64(windowSec) > duration {
		start = int(math.Floor(duration)) - windowSec
	}
	if start < 0 {
		start = 0
	}
	return start
}

// normalizeBandDB 将 -inf、超出物理下限的值与失败哨兵统一收敛到无信号地板
func normalizeBandDB(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, -1) || v == detectFailSentinel || v <= noSignalDBCap {
		return noSignalDBCap
	}
	return v
}

// bandProfile 各频段 RMS 能量 (dB)
type bandProfile struct {
	Full float64
	B16  float64
	B20  float64
	B22  float64
}

// bandVerdict 判定结论
type bandVerdict struct {
	Grade          LosslessGrade
	GradeText      string
	CutoffHz       int
	Confidence     int
	Details        string
	HighFreqEnergy float64
}

// classifyBands 依据频段能量与采样率给出判定（纯函数，便于单元测试）
// 判定统一采用"相对全频段衰减"而非绝对 dB 值：结果与录音电平、采样起点无关，
// 从而消除同一素材不同时长样本结论互相矛盾的 P1-1 缺陷。
func classifyBands(p bandProfile, sampleRate int) bandVerdict {
	d16 := p.Full - p.B16
	d20 := p.Full - p.B20
	d22 := p.Full - p.B22

	clamp := func(v float64) float64 {
		if math.IsNaN(v) || v < 0 {
			return 0
		}
		if v > displayCeilDB {
			return displayCeilDB
		}
		return v
	}

	switch {
	case d16 > cutoff16kThresholdDB:
		return bandVerdict{
			Grade:          GradeBadFake,
			GradeText:      "🚫 劣质假无损 (128k-192k 转压)",
			CutoffHz:       15800,
			Confidence:     95,
			Details:        fmt.Sprintf("16kHz 以上有效高频几乎缺失 (相对全频衰减 %.1fdB)，符合 128k~192k 有损编码硬截断特征", d16),
			HighFreqEnergy: clamp(d16),
		}
	case d22 > cutoff22kThresholdDB:
		return bandVerdict{
			Grade:          GradeSuspectFake,
			GradeText:      "⚠️ 假无损 (疑似有损转压)",
			CutoffHz:       20000,
			Confidence:     90,
			Details:        fmt.Sprintf("22kHz 以上频段近乎空白 (20kHz 以上衰减 %.1fdB，22kHz 以上 %.1fdB)，符合有损编码高频刀切特征", d20, d22),
			HighFreqEnergy: clamp(d22),
		}
	case sampleRate >= 88200 && d22 < hiresDiff22kThresholdDB:
		return bandVerdict{
			Grade:          GradeTrueHiRes,
			GradeText:      "🏆 真 Hi-Res 高解析度无损",
			CutoffHz:       sampleRate / 2,
			Confidence:     98,
			Details:        fmt.Sprintf("采样率 %dHz，高频自然延伸至 %.1fkHz 以上 (22kHz 以上相对衰减仅 %.1fdB)，属真高解析母带", sampleRate, float64(sampleRate)/2000.0, d22),
			HighFreqEnergy: clamp(d22),
		}
	default:
		return bandVerdict{
			Grade:          GradeTrueCD,
			GradeText:      "💎 真无损 (CD 音质)",
			CutoffHz:       22050,
			Confidence:     92,
			Details:        fmt.Sprintf("22kHz 附近泛音自然连贯 (20kHz 以上衰减 %.1fdB，22kHz 以上 %.1fdB)，符合标准 CD 无损声学特征", d20, d22),
			HighFreqEnergy: clamp(d22),
		}
	}
}

// measureBandEnergy 调用 FFmpeg astats 测量特定频段 RMS 能量 (dB)
func measureBandEnergy(ctx context.Context, ffmpegPath, filePath, audioFilter string, startSec, windowSec int) float64 {
	if windowSec <= 0 {
		windowSec = sampleWindowSec
	}
	val := measureBandEnergyAt(ctx, ffmpegPath, filePath, audioFilter, startSec, windowSec)
	if val == detectFailSentinel && startSec > 0 {
		// 起点超出音频实际时长时回退从头采样，避免短片/试听片段全部检测失败
		val = measureBandEnergyAt(ctx, ffmpegPath, filePath, audioFilter, 0, windowSec)
	}
	return val
}

// probeDurationSec 读取音频总时长（秒），优先使用与 ffmpeg 同目录的 ffprobe
func probeDurationSec(ctx context.Context, ffmpegPath, filePath string) (float64, error) {
	ffprobe := "ffprobe"
	if ffmpegPath != "" && ffmpegPath != "ffmpeg" {
		cand := filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
		if _, err := os.Stat(cand); err == nil {
			ffprobe = cand
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(cctx, ffprobe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1",
		filePath,
	).Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// measureBandEnergyAt 在指定起点采样并测量频段 RMS 能量 (dB)
func measureBandEnergyAt(ctx context.Context, ffmpegPath, filePath, audioFilter string, startSec, windowSec int) float64 {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{
		"-hide_banner",
		"-ss", strconv.Itoa(startSec),
		"-t", strconv.Itoa(windowSec),
		"-i", filePath,
		"-vn",
	}

	filterChain := "astats=metadata=1:reset=1"
	if audioFilter != "" {
		filterChain = audioFilter + "," + filterChain
	}
	args = append(args, "-af", filterChain, "-f", "null", "-")

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return detectFailSentinel
	}

	outStr := stderr.String()
	matches := rmsRe.FindAllStringSubmatch(outStr, -1)
	if len(matches) == 0 {
		return detectFailSentinel
	}

	// 取最后一个 RMS level
	lastMatch := matches[len(matches)-1]
	val, err := strconv.ParseFloat(lastMatch[1], 64)
	if err != nil {
		if strings.Contains(lastMatch[1], "-inf") {
			return noSignalDBCap
		}
		return detectFailSentinel
	}
	return val
}
