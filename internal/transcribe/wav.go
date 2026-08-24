package transcribe

import (
	"encoding/binary"
	"fmt"
)

// SampleRate is the canonical PCM rate for the whole pipeline: audio is
// normalized to 16kHz mono s16le once at ingest, everything downstream
// operates on that.
const SampleRate = 16000

const BytesPerSample = 2

// EncodeWAV wraps raw s16le mono PCM in a minimal 44-byte WAV header.
func EncodeWAV(pcm []byte, sampleRate int) []byte {
	dataLen := len(pcm)
	buf := make([]byte, 0, 44+dataLen)

	byteRate := sampleRate * BytesPerSample
	buf = append(buf, "RIFF"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+dataLen))
	buf = append(buf, "WAVE"...)
	buf = append(buf, "fmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)                 // fmt chunk size
	buf = binary.LittleEndian.AppendUint16(buf, 1)                  // PCM
	buf = binary.LittleEndian.AppendUint16(buf, 1)                  // mono
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate)) // sample rate
	buf = binary.LittleEndian.AppendUint32(buf, uint32(byteRate))   // byte rate
	buf = binary.LittleEndian.AppendUint16(buf, BytesPerSample)     // block align
	buf = binary.LittleEndian.AppendUint16(buf, 16)                 // bits per sample
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(dataLen))
	buf = append(buf, pcm...)
	return buf
}

// PCMFromWAV extracts the PCM payload from a WAV file by walking RIFF
// chunks (headers vary: ours are 44 bytes, ffmpeg's may carry LIST/fact
// chunks). Used to concatenate voiceprint samples into one reference clip.
func PCMFromWAV(wav []byte) ([]byte, error) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a WAV file")
	}
	pos := 12
	for pos+8 <= len(wav) {
		id := string(wav[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))
		if id == "data" {
			end := min(pos+8+size, len(wav))
			return wav[pos+8 : end], nil
		}
		pos += 8 + size + size%2 // chunks are word-aligned
	}
	return nil, fmt.Errorf("WAV has no data chunk")
}
