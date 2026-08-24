package transcribe

// Energy-based silence detection over s16le mono PCM, used to place chunk
// boundaries at pauses instead of mid-word: transcription quality at chunk
// edges is noticeably better when the cut lands in silence.

const (
	silenceFrameMs = 10  // energy is measured per 10ms frame
	silenceRunMs   = 300 // a "pause" is >=300ms of low energy
	// SilenceLevel is the mean-abs-amplitude threshold (of s16 range 32767)
	// under which a window counts as an actual pause, ~-36 dBFS. Typical
	// meeting-room noise floors sit well below speech but above digital
	// silence, so this is deliberately generous.
	SilenceLevel = 500.0
)

// QuietestCut scans pcm[from:to] and returns the sample-aligned byte offset
// at the center of the quietest 300ms window, plus that window's mean
// absolute amplitude. When the region is too small to hold a full window it
// returns the region midpoint with a level that never passes SilenceLevel,
// so callers treating the level as "did we find a pause" degrade safely.
func QuietestCut(pcm []byte, from, to int) (int, float64) {
	from = max(0, from) &^ 1
	to = min(len(pcm), to) &^ 1
	frameBytes := silenceFrameMs * SampleRate / 1000 * BytesPerSample
	windowFrames := silenceRunMs / silenceFrameMs
	nFrames := (to - from) / frameBytes
	if nFrames < windowFrames {
		return (from + (to-from)/2) &^ 1, 32767
	}

	// Mean |amplitude| per frame.
	energy := make([]float64, nFrames)
	for f := 0; f < nFrames; f++ {
		base := from + f*frameBytes
		var sum int64
		for i := 0; i < frameBytes; i += 2 {
			s := int16(uint16(pcm[base+i]) | uint16(pcm[base+i+1])<<8)
			if s < 0 {
				s = -s
			}
			sum += int64(s)
		}
		energy[f] = float64(sum) / float64(frameBytes/2)
	}

	// Sliding window: quietest run of windowFrames consecutive frames.
	var winSum float64
	for f := 0; f < windowFrames; f++ {
		winSum += energy[f]
	}
	bestStart, bestSum := 0, winSum
	for f := windowFrames; f < nFrames; f++ {
		winSum += energy[f] - energy[f-windowFrames]
		if winSum < bestSum {
			bestSum, bestStart = winSum, f-windowFrames+1
		}
	}
	cut := from + (bestStart+windowFrames/2)*frameBytes
	return cut &^ 1, bestSum / float64(windowFrames)
}
