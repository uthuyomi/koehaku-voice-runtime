package httptransport

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/tts"
)

type diagnosticTTSError struct{ data tts.SynthesisDiagnostic }

func (e diagnosticTTSError) Error() string                                { return "native synthesis failed" }
func (e diagnosticTTSError) SynthesisDiagnostic() tts.SynthesisDiagnostic { return e.data }

func TestTTSDiagnosticArtifactIsExplicitAndComplete(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YUKKURI_TTS_DIAGNOSTIC_DIR", "")
	err := diagnosticTTSError{tts.SynthesisDiagnostic{OriginalText: "元の文", ConvertedText: "モトノブン。",
		Voice: "f1", Speed: 100, NativeCode: 0}}
	writeTTSDiagnosticArtifact(err, "gen/unsafe", 7)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("artifact written without explicit opt-in")
	}

	t.Setenv("YUKKURI_TTS_DIAGNOSTIC_DIR", dir)
	writeTTSDiagnosticArtifact(errors.Join(errors.New("wrapped"), err), "gen/unsafe", 7)
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("entries=%d err=%v", len(entries), readErr)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var artifact map[string]any
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact["generation_id"] != "gen/unsafe" || artifact["sequence"] != float64(7) ||
		artifact["original_text"] != "元の文" || artifact["kanji2koe_output"] != "モトノブン。" ||
		artifact["native_error_code"] != float64(0) {
		t.Fatalf("unexpected artifact: %#v", artifact)
	}
}
