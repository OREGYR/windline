package main

import (
	"encoding/binary"
	"math"
	"syscall"
	"unsafe"
)

// sound.go is Sound.swift: the small sounds the line makes.
//
// The original plays two short WAV files it ships. Rather than carry binaries,
// the same two sounds are synthesized here at startup: a bright two-partial
// "tink" when a photo hangs, and a duller "pop" when one falls. Both are short
// and quiet, which is the point — they are a flourish, not an alert.

var (
	dWinmm     = syscall.NewLazyDLL("winmm.dll")
	pPlaySound = dWinmm.NewProc("PlaySoundW")
)

const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndMemory    = 0x0004
	sndNostop    = 0x0008
)

// soundBank holds the synthesized waves, played straight out of memory.
var soundBank = map[string][]byte{}

func soundStart() {
	soundBank["tink"] = wavTone(1180, 0.085, 0.30, 1.9)
	soundBank["pop"] = wavTone(420, 0.075, 0.26, 1.2)
	soundBank["beep"] = wavTone(660, 0.070, 0.22, 1.0)
}

// playSound plays one of the bank's sounds. Playback is asynchronous and
// non-stopping, so two photos landing at once do not cut each other off.
func playSound(kind string) {
	w, ok := soundBank[kind]
	if !ok || len(w) == 0 {
		return
	}
	// The buffer must stay alive for the duration of the asynchronous playback,
	// so it is kept in a package-level slot rather than passed as a temporary.
	keepAliveSound = w
	call(pPlaySound, uintptr(unsafe.Pointer(&keepAliveSound[0])), 0, sndMemory|sndAsync|sndNoDefault|sndNostop)
}

var keepAliveSound []byte

// wavTone builds a mono 16-bit PCM WAV in memory: a sine at freqHz with a
// second partial at ratio times the frequency, decaying over durationSec.
// A short attack keeps it from clicking.
func wavTone(freqHz, durationSec, gain, ratio float64) []byte {
	const rate = 44100
	n := int(float64(rate) * durationSec)
	samples := make([]int16, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(rate)
		// Exponential decay, fast enough that the sound is over quickly.
		env := math.Exp(-t / (durationSec * 0.28))
		// A 3 ms attack avoids a click at the start.
		if attack := 0.003; t < attack {
			env *= t / attack
		}
		s := math.Sin(2*math.Pi*freqHz*t) + 0.35*math.Sin(2*math.Pi*freqHz*ratio*t)
		v := s / 1.35 * env * gain
		if v > 1 {
			v = 1
		}
		if v < -1 {
			v = -1
		}
		samples[i] = int16(v * 32767)
	}
	return encodeWAV(samples, rate)
}

// encodeWAV wraps PCM samples in a canonical 44-byte WAV header.
func encodeWAV(samples []int16, rate int) []byte {
	dataBytes := len(samples) * 2
	out := make([]byte, 44+dataBytes)
	le := binary.LittleEndian
	copy(out[0:], "RIFF")
	le.PutUint32(out[4:], uint32(36+dataBytes))
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	le.PutUint32(out[16:], 16) // fmt chunk size
	le.PutUint16(out[20:], 1)  // PCM
	le.PutUint16(out[22:], 1)  // mono
	le.PutUint32(out[24:], uint32(rate))
	le.PutUint32(out[28:], uint32(rate*2)) // byte rate
	le.PutUint16(out[32:], 2)              // block align
	le.PutUint16(out[34:], 16)             // bits per sample
	copy(out[36:], "data")
	le.PutUint32(out[40:], uint32(dataBytes))
	for i, s := range samples {
		le.PutUint16(out[44+i*2:], uint16(s))
	}
	return out
}
