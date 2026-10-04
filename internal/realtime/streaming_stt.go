package realtime

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/stt"
)

func (s *Session) ConfigureStreamingSTT(p stt.StreamingProvider) error {
	if p == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streaming != nil {
		return context.Canceled
	}
	s.streaming = &streamingSTTRuntime{provider: p}
	return nil
}

func (s *Session) startStreamingTurnLocked(preRoll []byte) {
	r := s.streaming
	if r == nil || r.provider == nil || r.stream != nil {
		return
	}
	r.epoch++
	epoch, turn := r.epoch, s.input.turnID
	stream, err := r.provider.StartStream(s.ctx, stt.StreamRequest{SessionID: s.id, TurnID: turn, Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}})
	if err != nil {
		s.inputNotifyLocked(InputUpdate{State: s.input.state, TurnID: turn, Error: err.Error(), Reason: "streaming_stt_failed"})
		return
	}
	r.stream, r.turnID, r.latest, r.frozen, r.finalizing = stream, turn, nil, "", false
	if len(preRoll) > 0 {
		if err = stream.WritePCM(s.ctx, preRoll); err != nil {
			stream.Cancel()
			r.stream = nil
			return
		}
	}
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		first := true
		for {
			var result stt.StreamResult
			select {
			case result = <-stream.Results():
			case <-s.ctx.Done():
				return
			}
			s.mu.Lock()
			if s.streaming == r && r.stream == stream && r.epoch == epoch && r.turnID == turn && !result.Final {
				copyResult := result
				r.latest = &copyResult
				if first && strings.TrimSpace(result.Result.Text) != "" {
					first = false
					log.Printf("Nemotron first partial: session=%s turn=%s processed_ms=%.2f worker_lag_ms=%.2f tokens=%d", s.id, turn, float64(result.ProcessedSamples)*1000/16000, float64(result.WorkerLag.Microseconds())/1000, result.TokenCount)
				}
			}
			s.mu.Unlock()
			if result.Final {
				return
			}
		}
	}()
}

func (s *Session) startStreamingSpeculationLocked() bool {
	r := s.streaming
	if r == nil || r.stream == nil {
		return false
	}
	if r.latest == nil || strings.TrimSpace(r.latest.Result.Text) == "" {
		return true
	}
	r.frozen = strings.TrimSpace(r.latest.Result.Text)
	log.Printf("Nemotron speculative transcript freeze: session=%s turn=%s revision=%d text_bytes=%d speech_end_ms=%.2f", s.id, r.turnID, r.latest.Revision, len(r.frozen), float64(time.Since(r.vadEnd).Microseconds())/1000)
	s.startSpeculationFromTranscriptLocked(r.latest.Result)
	log.Printf("Nemotron speculative LLM start requested: session=%s turn=%s speech_end_ms=%.2f", s.id, r.turnID, float64(time.Since(r.vadEnd).Microseconds())/1000)
	return true
}

func (s *Session) beginStreamingFinalizeLocked(reason string) bool {
	r := s.streaming
	if r == nil || r.stream == nil {
		return false
	}
	if r.finalizing {
		return true
	}
	r.finalizing = true
	stream, epoch, turn, vadEnd := r.stream, r.epoch, r.turnID, r.vadEnd
	log.Printf("Nemotron Engine finalize request start: session=%s turn=%s epoch=%d speech_end_ms=%.2f", s.id, turn, epoch, float64(time.Since(vadEnd).Microseconds())/1000)
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		start := time.Now()
		// Final decoding is native CPU work and can legitimately exceed ten
		// seconds while already accepted audio is drained. Keep it bounded by
		// the same provider-scale deadline used elsewhere in the runtime.
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
		defer cancel()
		result, err := stream.Finalize(ctx)
		log.Printf("Nemotron Engine finalize response: session=%s turn=%s epoch=%d duration_ms=%.2f speech_end_ms=%.2f err=%v", s.id, turn, epoch, float64(time.Since(start).Microseconds())/1000, float64(time.Since(vadEnd).Microseconds())/1000, err)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.streaming != r || r.stream != stream || r.epoch != epoch || r.turnID != turn || s.input.turnID != turn {
			return
		}
		if err != nil || result == nil {
			s.inputNotifyLocked(InputUpdate{State: s.input.state, TurnID: turn, Error: "streaming STT finalization failed", Reason: "streaming_stt_failed"})
			s.cancelStreamingTurnLocked()
			return
		}
		log.Printf("Nemotron final: session=%s turn=%s finalization_ms=%.2f", s.id, turn, float64(time.Since(start).Microseconds())/1000)
		if r.frozen != "" && strings.TrimSpace(result.Text) != r.frozen {
			log.Printf("Nemotron speculation mismatch: session=%s turn=%s", s.id, turn)
			s.invalidateSpeculationLocked("transcript_mismatch")
		} else if r.frozen != "" {
			log.Printf("Nemotron speculation transcript match: session=%s turn=%s", s.id, turn)
		}
		r.stream = nil
		_ = stream.Close()
		s.commitTurnReadyLocked(reason, result)
		log.Printf("Nemotron transcript commit: session=%s turn=%s speech_end_ms=%.2f", s.id, turn, float64(time.Since(vadEnd).Microseconds())/1000)
	}()
	return true
}

func (s *Session) cancelStreamingTurnLocked() {
	if s.streaming == nil {
		return
	}
	s.streaming.epoch++
	if s.streaming.stream != nil {
		s.streaming.stream.Cancel()
	}
	s.streaming.stream = nil
	s.streaming.latest = nil
	s.streaming.frozen = ""
	s.streaming.finalizing = false
}
