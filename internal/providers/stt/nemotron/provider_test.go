package nemotron

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/stt"
)

func testProvider(t *testing.T, handler func(byte, uint64, []byte) wireResult) *Provider {
	t.Helper()
	client, worker := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	p := &Provider{ctx: ctx, cancel: cancel, conn: client, streams: map[uint64]*stream{}, epoch: 7, done: make(chan struct{}), cfg: Config{QueueChunks: 2}}
	go p.readLoop()
	go func() {
		defer worker.Close()
		for {
			var n uint32
			if binary.Read(worker, binary.LittleEndian, &n) != nil {
				return
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(worker, b); err != nil {
				return
			}
			response := handler(b[0], binary.LittleEndian.Uint64(b[1:9]), b[9:])
			if response.typeID == 0 {
				continue
			}
			payload := make([]byte, 41+len(response.text))
			payload[0] = response.typeID
			binary.LittleEndian.PutUint64(payload[1:9], response.id)
			binary.LittleEndian.PutUint64(payload[9:17], response.revision)
			binary.LittleEndian.PutUint32(payload[17:21], uint32(response.tokens))
			binary.LittleEndian.PutUint64(payload[21:29], response.processed)
			binary.LittleEndian.PutUint64(payload[29:37], response.queued)
			binary.LittleEndian.PutUint32(payload[37:41], uint32(len(response.text)))
			copy(payload[41:], response.text)
			_ = binary.Write(worker, binary.LittleEndian, uint32(len(payload)))
			_, _ = worker.Write(payload)
		}
	}()
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestStreamProtocolPartialFinalizeAndSequentialStreams(t *testing.T) {
	p := testProvider(t, func(kind byte, id uint64, pcm []byte) wireResult {
		switch kind {
		case msgPCM:
			return wireResult{typeID: msgPartial, id: id, revision: 1, tokens: 2, processed: uint64(len(pcm) / 2), text: "partial"}
		case msgFinalize:
			return wireResult{typeID: msgFinal, id: id, revision: 2, tokens: 2, processed: 1600, text: "final"}
		}
		return wireResult{}
	})
	for range 2 {
		s, err := p.StartStream(context.Background(), stt.StreamRequest{Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.WritePCM(context.Background(), make([]byte, 3200)); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-s.Results():
			if got.Result.Text != "partial" || got.ProcessedSamples != 1600 || got.Final {
				t.Fatalf("partial=%+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("partial timeout")
		}
		final, err := s.Finalize(context.Background())
		if err != nil || final.Text != "final" {
			t.Fatalf("final=%+v err=%v", final, err)
		}
		_ = s.Close()
	}
}

func TestStreamRejectsOversizedFrameAndWorkerDisconnect(t *testing.T) {
	p := testProvider(t, func(kind byte, id uint64, _ []byte) wireResult {
		return wireResult{typeID: msgError, id: id, text: "worker error"}
	})
	s, err := p.StartStream(context.Background(), stt.StreamRequest{Format: stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WritePCM(context.Background(), make([]byte, maxFrame)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	s.Cancel()
}
