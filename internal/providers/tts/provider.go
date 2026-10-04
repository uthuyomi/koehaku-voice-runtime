package tts

import (
	"context"
	"errors"
	"io"
)

// ErrNoSpeech means the provider input has no pronounceable symbols. A caller
// may omit that semantic chunk without failing the surrounding generation.
var ErrNoSpeech = errors.New("TTS input contains no pronounceable symbols")

// Request はTTSエンジンへ渡す共通リクエスト。
// AquesTalk固有の設定はProvider側へ閉じ込める。
type Request struct {
	Text  string
	Voice string
	Speed float64
}

// AudioFormat はProviderが返す音声形式。
type AudioFormat struct {
	Codec      string
	SampleRate int
	Channels   int
}

// Stream は生成された音声ストリーム。
type Stream struct {
	Format AudioFormat
	Audio  io.ReadCloser
}

// Provider はすべてのTTS Providerが実装する共通interface。
type Provider interface {
	Name() string
	Synthesize(ctx context.Context, req Request) (*Stream, error)
}

// SynthesisDiagnostic is available only from provider failures. Callers must
// keep its text fields out of normal logs and user-visible protocol errors.
type SynthesisDiagnostic struct {
	OriginalText  string
	ConvertedText string
	Voice         string
	Speed         int
	NativeCode    int32
}

type DiagnosticError interface {
	error
	SynthesisDiagnostic() SynthesisDiagnostic
}
