package transcribe

import (
	"encoding/binary"
	"testing"
)

// tone writes loudAmp samples except for a silent gap at [gapFrom, gapTo).
func tonePCM(totalS float64, gapFromS, gapToS float64) []byte {
	n := int(totalS * SampleRate)
	pcm := make([]byte, n*2)
	for i := 0; i < n; i++ {
		amp := int16(8000)
		tS := float64(i) / SampleRate
		if tS >= gapFromS && tS < gapToS {
			amp = 50
		}
		if i%2 == 1 {
			amp = -amp
		}
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(amp))
	}
	return pcm
}

func TestQuietestCutFindsPause(t *testing.T) {
	// 20s of speech with a 0.5s pause at 12.0-12.5s.
	pcm := tonePCM(20, 12.0, 12.5)
	cut, level := QuietestCut(pcm, 0, len(pcm))
	cutS := float64(cut) / float64(SampleRate*BytesPerSample)
	if cutS < 12.0 || cutS > 12.5 {
		t.Errorf("cut at %.2fs, want inside the 12.0-12.5s pause", cutS)
	}
	if level > SilenceLevel {
		t.Errorf("pause level %.0f should pass SilenceLevel %.0f", level, SilenceLevel)
	}
	if cut%2 != 0 {
		t.Errorf("cut %d not sample-aligned", cut)
	}
}

func TestQuietestCutContinuousSpeech(t *testing.T) {
	pcm := tonePCM(10, -1, -1) // no pause anywhere
	_, level := QuietestCut(pcm, 0, len(pcm))
	if level <= SilenceLevel {
		t.Errorf("continuous speech level %.0f must not pass SilenceLevel", level)
	}
}

func TestQuietestCutTinyRegion(t *testing.T) {
	pcm := tonePCM(1, -1, -1)
	cut, level := QuietestCut(pcm, 100, 200) // 50 samples: smaller than one 300ms window
	if cut < 100 || cut > 200 {
		t.Errorf("degenerate cut %d outside region", cut)
	}
	if level <= SilenceLevel {
		t.Error("degenerate region must not report silence")
	}
}
