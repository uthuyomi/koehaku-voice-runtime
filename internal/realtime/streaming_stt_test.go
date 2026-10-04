package realtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/stt"
	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/stt/limited"
	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/turndetection"
)

type testStreamingProvider struct {
	stream    *testSTTStream
	finalText string
}

func (*testStreamingProvider) Name() string { return "streaming-test" }
func (*testStreamingProvider) Transcribe(context.Context, stt.Request) (*stt.Result, error) {
	return &stt.Result{Text: "fallback"}, nil
}
func (p *testStreamingProvider) StartStream(context.Context, stt.StreamRequest) (stt.Stream, error) {
	p.stream = &testSTTStream{results: make(chan stt.StreamResult, 4), finalText: p.finalText}
	return p.stream, nil
}

type testSTTStream struct {
	results   chan stt.StreamResult
	cancelled atomic.Bool
	budgetNS  atomic.Int64
	finalText string
}

func (s *testSTTStream) WritePCM(context.Context, []byte) error {
	select {
	case s.results <- stt.StreamResult{Result: stt.Result{Text: "stream transcript"}, Revision: 1, TokenCount: 2, ProcessedSamples: 1600}:
	default:
	}
	return nil
}
func (s *testSTTStream) Results() <-chan stt.StreamResult { return s.results }
func (s *testSTTStream) Finalize(ctx context.Context) (*stt.Result, error) {
	if deadline, ok := ctx.Deadline(); ok {
		s.budgetNS.Store(int64(time.Until(deadline)))
	}
	text := s.finalText
	if text == "" {
		text = "stream transcript"
	}
	return &stt.Result{Text: text}, nil
}
func (s *testSTTStream) Cancel()      { s.cancelled.Store(true) }
func (s *testSTTStream) Close() error { return nil }

func TestStreamingFastPathWaitsForVADEndAndCommit(t *testing.T) {
	p := &testStreamingProvider{}
	s := NewSession(context.Background())
	defer s.Close()
	gate := make(chan struct{})
	if err := s.ConfigureInput(detectorFunc(func(ctx context.Context, _ turndetection.Request) (turndetection.Result, error) {
		select {
		case <-gate:
			return turndetection.Result{Complete: true}, nil
		case <-ctx.Done():
			return turndetection.Result{}, ctx.Err()
		}
	}), EndpointConfig{10 * time.Millisecond, time.Second, 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureSpeculation(limited.New(p, 1), goodSpecLLM(), specConfig()); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureStreamingSTT(p); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRealtimeInput(InputAudioFormatData{Mode: "realtime", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SpeechStart(); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRealtimeAudio(make([]byte, 3200)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	hasSpeculation := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.speculation.current != nil
	}
	if hasSpeculation() {
		t.Fatal("partial started speculation before VAD end")
	}
	if err := s.SpeechEnd(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !hasSpeculation() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !hasSpeculation() {
		t.Fatal("VAD end did not start speculation")
	}
	close(gate)
	u := nextCommit(t, s)
	if u.Transcript == nil || u.Transcript.Text != "stream transcript" {
		t.Fatalf("final=%+v", u.Transcript)
	}
	if u.SpeculationKey == nil {
		t.Fatal("matching final transcript did not preserve promotion candidate")
	}
	if budget := time.Duration(p.stream.budgetNS.Load()); budget < time.Minute {
		t.Fatalf("native finalization budget is too short: %s", budget)
	}
}

func TestStreamingFinalMismatchInvalidatesSpeculation(t *testing.T) {
	p := &testStreamingProvider{finalText: "different final"}
	s := NewSession(context.Background())
	defer s.Close()
	gate := make(chan struct{})
	if err := s.ConfigureInput(detectorFunc(func(ctx context.Context, _ turndetection.Request) (turndetection.Result, error) {
		select {
		case <-gate:
			return turndetection.Result{Complete: true}, nil
		case <-ctx.Done():
			return turndetection.Result{}, ctx.Err()
		}
	}), EndpointConfig{10 * time.Millisecond, time.Second, 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureSpeculation(limited.New(p, 1), goodSpecLLM(), specConfig()); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureStreamingSTT(p); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRealtimeInput(InputAudioFormatData{Mode: "realtime", SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}); err != nil {
		t.Fatal(err)
	}
	_ = s.SpeechStart()
	_ = s.AppendRealtimeAudio(make([]byte, 3200))
	time.Sleep(20 * time.Millisecond)
	_ = s.SpeechEnd()
	close(gate)
	u := nextCommit(t, s)
	if u.Transcript == nil || u.Transcript.Text != "different final" {
		t.Fatalf("final=%+v", u.Transcript)
	}
	if u.SpeculationKey != nil {
		t.Fatal("mismatched transcript retained promotion candidate")
	}
}
