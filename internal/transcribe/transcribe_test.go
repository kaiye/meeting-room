package transcribe

import (
	"encoding/binary"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:7860":      "http://127.0.0.1:7860/v1",
		"http://127.0.0.1:7860/":     "http://127.0.0.1:7860/v1",
		"http://127.0.0.1:7860/v1":   "http://127.0.0.1:7860/v1",
		"http://127.0.0.1:7860/v1/":  "http://127.0.0.1:7860/v1",
		" https://api.example.com/ ": "https://api.example.com/v1",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDiarizedResponse(t *testing.T) {
	payload := []byte(`{"task":"transcribe","segments":[
		{"id":"seg-0","type":"transcript.text.segment","start":0.5,"end":3.2,"speaker":"S01","text":" 大家好 "},
		{"id":"seg-1","type":"transcript.text.segment","start":3.4,"end":5.0,"speaker":"S02","text":"你好"},
		{"id":"seg-2","type":"transcript.text.segment","start":5.1,"end":5.2,"speaker":"S01","text":"  "}
	],"text":"大家好 你好"}`)
	segs, err := ParseResponse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("expected 2 non-empty segments, got %d", len(segs))
	}
	if segs[0].Speaker != "S01" || segs[0].Text != "大家好" {
		t.Errorf("unexpected first segment: %+v", segs[0])
	}
	if *segs[1].StartS != 3.4 || *segs[1].EndS != 5.0 {
		t.Errorf("unexpected timestamps: %+v", segs[1])
	}
}

func TestParsePlainJSONResponse(t *testing.T) {
	segs, err := ParseResponse([]byte(`{"text":" hello world "}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Text != "hello world" || segs[0].StartS != nil {
		t.Fatalf("unexpected plain-json result: %+v", segs)
	}
}

func TestEncodeWAVHeader(t *testing.T) {
	pcm := make([]byte, 32000) // 1 second @ 16kHz s16le
	wav := EncodeWAV(pcm, SampleRate)
	if len(wav) != 44+len(pcm) {
		t.Fatalf("wav length = %d, want %d", len(wav), 44+len(pcm))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatal("bad RIFF/WAVE magic")
	}
	if rate := binary.LittleEndian.Uint32(wav[24:28]); rate != 16000 {
		t.Errorf("sample rate = %d, want 16000", rate)
	}
	if dataLen := binary.LittleEndian.Uint32(wav[40:44]); dataLen != uint32(len(pcm)) {
		t.Errorf("data length = %d, want %d", dataLen, len(pcm))
	}
}

func TestAbsoluteSegments(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	// Second 10-minute chunk: offset 600s, chunk length 600s.
	segs := AbsoluteSegments(600, 600, []Segment{
		{Text: "a", Speaker: "S01", StartS: f(1.5), EndS: f(4.0)},
		{Text: "b"}, // no timestamps -> chunk boundaries
	})
	if segs[0].StartMs != 601500 || segs[0].EndMs != 604000 {
		t.Errorf("timed segment rebased wrong: %+v", segs[0])
	}
	if segs[1].StartMs != 600000 || segs[1].EndMs != 1200000 {
		t.Errorf("untimed segment fallback wrong: %+v", segs[1])
	}
}
