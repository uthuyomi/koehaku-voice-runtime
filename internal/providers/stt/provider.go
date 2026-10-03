package stt

import (
	"context"
	"time"
)

type AudioFormat struct {
	SampleRate int
	Channels   int
	Encoding   string
}

type Request struct {
	Audio  []byte
	Format AudioFormat
}

type Result struct {
	Text       string
	Confidence float64
	Language   string
}

type Provider interface {
	Name() string

	Transcribe(
		ctx context.Context,
		req Request,
	) (*Result, error)
}

// StreamRequest identifies one logical utterance. Implementations must keep
// model state outside the stream so creating a stream never reloads a model.
type StreamRequest struct {
	SessionID string
	TurnID    string
	Format    AudioFormat
}

type StreamResult struct {
	Result           Result
	StreamID         uint64
	Epoch            uint64
	Revision         uint64
	TokenCount       int
	ProcessedSamples uint64
	QueuedSamples    uint64
	WorkerLag        time.Duration
	Final            bool
}

type Stream interface {
	WritePCM(context.Context, []byte) error
	Results() <-chan StreamResult
	Finalize(context.Context) (*Result, error)
	Cancel()
	Close() error
}

// StreamingProvider is optional. Provider-only implementations retain the
// existing final-only path.
type StreamingProvider interface {
	Provider
	StartStream(context.Context, StreamRequest) (Stream, error)
}
