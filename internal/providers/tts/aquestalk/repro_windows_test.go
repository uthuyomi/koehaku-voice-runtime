//go:build windows

package aquestalk

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/tts"
)

func testProvider(t *testing.T) *Provider {
	t.Helper()
	root := filepath.FromSlash("aqtk1_win/lib64/f1/AquesTalk.dll")
	if _, err := os.Stat(root); err != nil {
		t.Skip("local AquesTalk assets are unavailable")
	}
	p, err := New(Config{Voices: map[string]string{"f1": root}, DefaultVoice: "f1",
		Kanji2KoeDLL: filepath.FromSlash("aqk2k_win/lib64/AqKanji2Koe.dll"),
		Kanji2KoeDic: filepath.FromSlash("aqk2k_win/aq_dic")})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func synthesize(t *testing.T, p *Provider, text string) error {
	t.Helper()
	s, err := p.Synthesize(context.Background(), tts.Request{Text: text, Voice: "f1", Speed: 1})
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, s.Audio)
	closeErr := s.Audio.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func TestSequentialNativeCalls100(t *testing.T) {
	p := testProvider(t)
	defer p.Close()
	for i := 0; i < 100; i++ {
		if err := synthesize(t, p, "これは連続合成の再現試験です。句読点、改行も確認します。\n次の文です。"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}

func TestWhitespaceOnlyChunkHasNoSpeech(t *testing.T) {
	p := testProvider(t)
	defer p.Close()
	_, err := p.Synthesize(context.Background(), tts.Request{Text: "\n\n\n", Voice: "f1", Speed: 1})
	if !errors.Is(err, tts.ErrNoSpeech) {
		t.Fatalf("error=%v, want ErrNoSpeech", err)
	}
}

func TestConcurrentNativeCallsAreSerialized(t *testing.T) {
	p := testProvider(t)
	defer p.Close()
	var failures atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := synthesize(t, p, "これは並行合成の再現試験です。"); err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("failures=%d", n)
	}
}
