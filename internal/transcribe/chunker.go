package transcribe

import (
	"context"
	"fmt"
	"time"
)

// GapText marks a chunk that failed transcription after retries; the rest
// of the recording continues instead of aborting the whole job.
const GapText = "(该时段转录失败,可稍后重新转录)"

// TimedSegment is a transcript segment with absolute timestamps in the
// full recording (not chunk-relative). WindowIdx identifies the chunk it
// came from — raw cluster labels are only comparable within one window.
type TimedSegment struct {
	Text      string
	Speaker   string
	StartMs   int64
	EndMs     int64
	WindowIdx int
}

// TranscribeWithRetry submits one WAV payload with retries (1s/2s backoff):
// per the provider guidance, transient 5xx/timeouts deserve two retries
// before the chunk is declared lost.
func TranscribeWithRetry(ctx context.Context, c *Client, wav []byte, audioSeconds float64, language string, attempts int) ([]Segment, error) {
	var lastErr error
	for a := 0; a < attempts; a++ {
		if a > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(a) * time.Second):
			}
		}
		segs, err := c.TranscribeWAV(ctx, wav, audioSeconds, language)
		if err == nil {
			return segs, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// TranscribeAll splits normalized PCM into chunks and submits them serially,
// converting each chunk's service-relative timestamps to absolute positions.
// Serial submission is deliberate: GPU services hold a single inference lock
// (parallel requests just queue) and speaker labels are only consistent
// within one request. Chunk boundaries snap to the quietest pause within
// the last 10s of each chunk; a failed chunk (after retries) becomes a gap
// marker instead of failing the job. progress is called after each chunk.
func TranscribeAll(ctx context.Context, c *Client, pcm []byte, language string, chunkSeconds int, progress func(doneChunks, totalChunks int)) ([]TimedSegment, error) {
	if chunkSeconds <= 0 {
		chunkSeconds = 600
	}
	bytesPerSec := SampleRate * BytesPerSample
	chunkBytes := chunkSeconds * bytesPerSec
	lookbackBytes := min(10*bytesPerSec, chunkBytes/2)
	totalChunks := (len(pcm) + chunkBytes - 1) / chunkBytes
	if totalChunks == 0 {
		return nil, nil
	}

	var out []TimedSegment
	var lastErr error
	done, failed := 0, 0
	for start := 0; start < len(pcm); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := start + chunkBytes
		if end >= len(pcm) {
			end = len(pcm)
		} else if cut, _ := QuietestCut(pcm, end-lookbackBytes, end); cut > start {
			// Batch mode always takes the quietest point near the boundary —
			// it can only be better than a hard cut.
			end = cut
		}
		chunk := pcm[start:end]

		offsetS := float64(start) / float64(bytesPerSec)
		chunkDurS := float64(len(chunk)) / float64(bytesPerSec)

		segs, err := TranscribeWithRetry(ctx, c, EncodeWAV(chunk, SampleRate), chunkDurS, language, 3)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failed++
			lastErr = fmt.Errorf("chunk %d (offset %.0fs): %w", done+1, offsetS, err)
			out = append(out, TimedSegment{
				Text:      GapText,
				StartMs:   int64(offsetS * 1000),
				EndMs:     int64((offsetS + chunkDurS) * 1000),
				WindowIdx: done,
			})
		} else {
			timed := AbsoluteSegments(offsetS, chunkDurS, segs)
			for i := range timed {
				timed[i].WindowIdx = done
			}
			out = append(out, timed...)
		}
		done++
		if progress != nil {
			progress(done, max(done, totalChunks))
		}
		start = end
	}
	if failed > 0 && failed == done {
		return nil, fmt.Errorf("所有音频块转录均失败,最后一个错误: %w", lastErr)
	}
	return out, nil
}

// AbsoluteSegments rebases chunk-relative segment timestamps onto the full
// recording timeline. Segments without timestamps (plain-json services)
// fall back to the chunk boundaries.
func AbsoluteSegments(offsetS, chunkDurS float64, segs []Segment) []TimedSegment {
	out := make([]TimedSegment, 0, len(segs))
	for _, s := range segs {
		startS, endS := offsetS, offsetS+chunkDurS
		if s.StartS != nil {
			startS = offsetS + *s.StartS
		}
		if s.EndS != nil {
			endS = offsetS + *s.EndS
		}
		out = append(out, TimedSegment{
			Text:    s.Text,
			Speaker: s.Speaker,
			StartMs: int64(startS * 1000),
			EndMs:   int64(endS * 1000),
		})
	}
	return out
}
