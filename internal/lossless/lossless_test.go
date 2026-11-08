package lossless

import (
	"math"
	"testing"
)

// 数据来源：NAS 实机 ffmpeg astats 实测（粉噪声 44.1kHz）
// 真无损：22kHz 以上相对衰减 33~48dB；128k/192k/320k 有损转压：89~101dB
func TestClassifyBandsCalibration(t *testing.T) {
	cases := []struct {
		name   string
		p      bandProfile
		sr     int
		want   LosslessGrade
		maxEne float64
	}{
		{
			name: "true60_起始0s",
			p:    bandProfile{Full: -20.94, B16: -37.60, B20: -44.54, B22: -68.84},
			sr:   44100, want: GradeTrueCD, maxEne: 100,
		},
		{
			name: "true60_起始20s",
			p:    bandProfile{Full: -22.31, B16: -37.39, B20: -44.05, B22: -65.22},
			sr:   44100, want: GradeTrueCD, maxEne: 100,
		},
		{
			name: "true5_短片段",
			p:    bandProfile{Full: -26.19, B16: -39.24, B20: -44.28, B22: -60.07},
			sr:   44100, want: GradeTrueCD, maxEne: 100,
		},
		{
			name: "fake60_128k转压",
			p:    bandProfile{Full: -24.72, B16: -38.53, B20: -49.78, B22: -113.49},
			sr:   44100, want: GradeSuspectFake, maxEne: 100,
		},
		{
			name: "fake5_128k转压",
			p:    bandProfile{Full: -26.81, B16: -45.35, B20: -63.24, B22: -127.71},
			sr:   44100, want: GradeSuspectFake, maxEne: 100,
		},
		{
			name: "src320_320k转压",
			p:    bandProfile{Full: -22.29, B16: -37.98, B20: -48.42, B22: -111.68},
			sr:   44100, want: GradeSuspectFake, maxEne: 100,
		},
		{
			name: "src192_192k转压",
			p:    bandProfile{Full: -22.55, B16: -38.22, B20: -48.65, B22: -112.03},
			sr:   44100, want: GradeSuspectFake, maxEne: 100,
		},
		{
			name: "aac256_近透明",
			p:    bandProfile{Full: -22.36, B16: -37.49, B20: -44.68, B22: -65.13},
			sr:   44100, want: GradeTrueCD, maxEne: 100,
		},
		{
			// 440Hz 纯音：20k/22k 无信号归一为 -120 后衰减有限，不得输出 977.9dB
			name: "纯音real.flac_无高频信号",
			p:    bandProfile{Full: -21.08, B16: -103.00, B20: noSignalDBCap, B22: noSignalDBCap},
			sr:   44100, want: GradeBadFake, maxEne: 100,
		},
		{
			// 同一素材 60s 与 5s 必须一致（P1-1 回归）
			name: "hires_96k真母带",
			p:    bandProfile{Full: -20.00, B16: -33.00, B20: -38.00, B22: -46.00},
			sr:   96000, want: GradeTrueHiRes, maxEne: 100,
		},
	}

	for _, c := range cases {
		v := classifyBands(c.p, c.sr)
		if v.Grade != c.want {
			t.Errorf("%s: grade=%s want=%s (details=%s)", c.name, v.Grade, c.want, v.Details)
		}
		if v.HighFreqEnergy < 0 || v.HighFreqEnergy > c.maxEne {
			t.Errorf("%s: high_freq_energy=%v 超出合理范围", c.name, v.HighFreqEnergy)
		}
		if v.Details == "" || v.GradeText == "" {
			t.Errorf("%s: 文案为空", c.name)
		}
	}
}

// 同一素材不同采样起点、不同时长必须给出相同结论
func TestClassifyConsistencyAcrossOffsets(t *testing.T) {
	pairs := [][2]bandProfile{
		{
			{Full: -20.94, B16: -37.60, B20: -44.54, B22: -68.84},
			{Full: -22.31, B16: -37.39, B20: -44.05, B22: -65.22},
		},
		{
			{Full: -24.72, B16: -38.53, B20: -49.78, B22: -113.49},
			{Full: -23.82, B16: -38.69, B20: -48.62, B22: -111.87},
		},
	}
	for i, pr := range pairs {
		a := classifyBands(pr[0], 44100)
		b := classifyBands(pr[1], 44100)
		if a.Grade != b.Grade {
			t.Errorf("pair%d: %s vs %s 结论不一致", i, a.Grade, b.Grade)
		}
	}
}

func TestNormalizeBandDB(t *testing.T) {
	cases := map[string]float64{
		"-999 哨兵": detectFailSentinel,
		"-inf":    math.Inf(-1),
		"NaN":     math.NaN(),
		"-130":    -130,
	}
	for name, in := range cases {
		if got := normalizeBandDB(in); got != noSignalDBCap {
			t.Errorf("%s: got %v want %v", name, got, noSignalDBCap)
		}
	}
	if got := normalizeBandDB(-45.5); got != -45.5 {
		t.Errorf("正常值被改写: %v", got)
	}
}

func TestChooseSampleStart(t *testing.T) {
	cases := []struct {
		dur  float64
		want int
	}{
		{5, 0},
		{25, 0},
		{30, 5},
		{60, 21},
		{180, 63},
	}
	for _, c := range cases {
		if got := chooseSampleStart(c.dur, sampleWindowSec); got != c.want {
			t.Errorf("dur=%v: got %d want %d", c.dur, got, c.want)
		}
	}
	// 任意时长下窗口都必须完整落在音频内
	for dur := 26.0; dur <= 300; dur += 7.3 {
		st := chooseSampleStart(dur, sampleWindowSec)
		if float64(st)+float64(sampleWindowSec) > dur+0.001 {
			t.Errorf("dur=%v: start=%d 窗口越界", dur, st)
		}
	}
}

// 展示数值永不为 -999 / ±Inf
func TestHighFreqEnergyNeverSentinel(t *testing.T) {
	ps := []bandProfile{
		{Full: -21.08, B16: detectFailSentinel, B20: detectFailSentinel, B22: detectFailSentinel},
		{Full: -21.08, B16: math.Inf(-1), B20: math.Inf(-1), B22: math.Inf(-1)},
		{Full: -6.0, B16: noSignalDBCap, B20: noSignalDBCap, B22: noSignalDBCap},
	}
	for _, p := range ps {
		np := bandProfile{
			Full: p.Full,
			B16:  normalizeBandDB(p.B16),
			B20:  normalizeBandDB(p.B20),
			B22:  normalizeBandDB(p.B22),
		}
		v := classifyBands(np, 44100)
		if math.IsNaN(v.HighFreqEnergy) || math.IsInf(v.HighFreqEnergy, 0) {
			t.Fatalf("非法数值: %v", v.HighFreqEnergy)
		}
		if v.HighFreqEnergy == detectFailSentinel || v.HighFreqEnergy < 0 || v.HighFreqEnergy > displayCeilDB {
			t.Fatalf("数值越界: %v", v.HighFreqEnergy)
		}
	}
}
