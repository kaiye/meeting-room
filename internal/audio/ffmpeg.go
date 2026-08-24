// Package audio normalizes uploaded media into the pipeline's canonical
// PCM format by shelling out to ffmpeg. This replaces the desktop app's
// in-process Rust decoding: on a server, ffmpeg handles every container
// and codec users will throw at us.
package audio

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// NormalizeToPCM16k decodes any ffmpeg-supported input file into raw
// 16kHz mono s16le PCM and returns it with the duration in seconds.
func NormalizeToPCM16k(ctx context.Context, inputPath string) ([]byte, float64, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", inputPath,
		"-ac", "1",
		"-ar", "16000",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, 0, fmt.Errorf("ffmpeg failed: %v: %s", err, stderr.String())
	}
	pcm := stdout.Bytes()
	if len(pcm) == 0 {
		return nil, 0, fmt.Errorf("ffmpeg produced no audio (unsupported or empty file?)")
	}
	duration := float64(len(pcm)) / (2 * 16000)
	return pcm, duration, nil
}

// TruncateAudio cuts the file down to its first `seconds` seconds in
// place (stream copy, no re-encode). The previous full version is kept
// next to it as *.bak.* — one level of undo for a destructive edit.
func TruncateAudio(ctx context.Context, path string, seconds float64) error {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	tmp := base + ".trim" + ext
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", path,
		"-t", fmt.Sprintf("%.3f", seconds),
		"-c", "copy",
		"-y", tmp,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg truncate failed: %v: %s", err, stderr.String())
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() < 1024 {
		os.Remove(tmp)
		return fmt.Errorf("截断产物为空")
	}
	bak := base + ".bak" + ext
	os.Remove(bak)
	if err := os.Rename(path, bak); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RawPCMToM4A encodes a raw s16le 16kHz mono PCM file into AAC/m4a for
// long-term storage: ~0.35MB/min vs 1.8MB/min for WAV, encodes at ~550x
// realtime, and every browser's <audio> plays it natively.
func RawPCMToM4A(ctx context.Context, pcmPath, outPath string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "s16le", "-ar", "16000", "-ac", "1",
		"-i", pcmPath,
		"-c:a", "aac", "-b:a", "48k",
		"-y", outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg m4a encode failed: %v: %s", err, stderr.String())
	}
	if fi, err := os.Stat(outPath); err != nil || fi.Size() < 1024 {
		return fmt.Errorf("ffmpeg produced an empty m4a")
	}
	return nil
}

// ExtractClipWAV cuts [startS, startS+durS) out of any audio file as a
// 16kHz mono WAV — used to turn a labeled transcript segment into a
// speaker's voiceprint reference clip.
func ExtractClipWAV(ctx context.Context, inputPath string, startS, durS float64) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", startS),
		"-t", fmt.Sprintf("%.3f", durS),
		"-i", inputPath,
		"-ac", "1",
		"-ar", "16000",
		"-f", "wav",
		"-",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg clip failed: %v: %s", err, stderr.String())
	}
	if stdout.Len() < 100 {
		return nil, fmt.Errorf("截取的音频为空(时间范围超出录音?)")
	}
	return stdout.Bytes(), nil
}
