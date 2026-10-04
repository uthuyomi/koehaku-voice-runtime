# Koehaku Voice Runtime

Open-source local-first realtime voice runtime for AI.

Koehaku combines *koe* ("voice") and *haku* ("beat/timing"), reflecting the timing and flow required for realtime conversation.

English | [日本語](README.ja.md)

Koehaku Voice Runtime is a self-hosted realtime conversational voice runtime. It connects microphone PCM, STT, turn detection, speculative and streaming LLM work, speech chunking, TTS, and playback feedback through one session lifecycle.

The engine supports the existing whisper.cpp path and a dedicated Nemotron 3.5 Streaming ASR fast path. STT, LLM, turn detection, and TTS remain provider boundaries. The sample uses an external OpenAI-compatible LLM service and separately obtained AquesTalk assets. No proprietary assets, models, or credentials are included.

## Runtime flow

```text
Microphone -> VAD / PCM -> STT -> Smart Turn -> conversation commit
                                      |              |
                                      +-> speculation+-> streaming LLM
                                                        -> speech chunks
                                                        -> TTS -> PCM
                                                        -> AudioWorklet
```

The realtime path supports barge-in, generation-scoped cancellation, stale-event isolation, bounded audio ingress, `credit-v1` playback flow control, and playback-aware conversation history. Smart Turn remains authoritative for commit decisions; speculative output is not published before promotion.

## Quick start

The engine target is Windows x64. Building requires the Go version declared in [`go.mod`](go.mod). Browser Voice development also needs Node.js; Smart Turn and the Python SDK need Python. Runtime binaries, models, AquesTalk/AqKanji2Koe assets, and API credentials must be supplied locally.

```powershell
git clone https://github.com/uthuyomi/koehaku-voice-runtime.git
cd koehaku-voice-runtime
Copy-Item .env.example .env
go build -o dist/engine.exe ./cmd/engine
```

Configure providers in `.env`, then use the development launcher:

```powershell
./scripts/dev.ps1 whisper
./scripts/dev.ps1 nemotron
```

The launcher starts the Engine, Smart Turn sidecar, and Browser Voice server. It does not start a second Nemotron worker; the Engine owns its persistent worker. Browser Voice is available at `http://127.0.0.1:8080/examples/typescript/browser-voice/`. Press Ctrl+C once to shut down the launcher-owned process tree.

See [Getting Started](docs/quickstart.md), [Configuration](docs/configuration.md), [Provider setup](docs/providers.md), and [Troubleshooting](docs/troubleshooting.md).

## Public interfaces

- `GET /health`
- `GET /v1/capabilities`
- `POST /v1/audio/speech`
- `WS /v1/realtime`
- `WS /v1/transcription`
- [TypeScript SDK](sdk/typescript/README.md)
- [Python SDK](sdk/python/README.md)

See [API](docs/api.md), [Realtime protocol](docs/realtime-protocol.md), and [Protocol versioning](docs/protocol-versioning.md).

## Benchmarks

The benchmark suite uses the same generated bilingual corpus and public transcription WebSocket for both STT providers. It separates accuracy, finalization latency, full-pipeline TTFA, stability, and resources. Generated speech is a deterministic test input, not a substitute for physical-microphone acceptance.

- [Whisper vs Nemotron](docs/en/benchmarks/whisper-vs-nemotron.md)
- [Methodology](docs/benchmarks/methodology.md)
- [Phase 3/3B investigation](docs/benchmarks/realtime-performance-investigation-2026-10-04.md)
- [Machine-readable results](benchmark-results/2026-10-04/)

The established Nemotron Phase 3B 100-turn run completed 100/100 turns with no reported worker/provider errors, queue overflows, dropped audio, playback failures, AquesTalk errors, or AudioWorklet underruns. Final ASR was 1.019 s mean / 0.915 s p50 / 1.813 s p95 / 3.317 s max; first render was 1.831 s mean / 1.594 s p50 / 3.327 s p95 / 4.680 s max. These values apply to the documented machine, models, fixtures, and definitions. First render is not a microphone-to-speaker audible measurement.

## Development

```powershell
go test ./...
go build ./cmd/engine
npm --prefix sdk/typescript test
py -3.12 -m unittest discover -s sdk/python/tests
```

Read [CONTRIBUTING.md](CONTRIBUTING.md) before changing the runtime. Coding agents should start with [AGENTS.md](AGENTS.md); Japanese maintainers can use its [companion guide](docs/ja/ai-maintenance-guide.md).

## Licensing and security

Project-authored code and documentation are covered by [MIT](LICENSE). Third-party runtimes, models, and proprietary voice assets retain their own terms; see [Third-party notices](THIRD_PARTY_NOTICES.md). Never commit `.env`, credentials, model files, runtime binaries, or proprietary AQUEST assets. Report vulnerabilities through [SECURITY.md](SECURITY.md).

This repository is under local public-release review. Version selection, tags, releases, and publication remain owner decisions.
