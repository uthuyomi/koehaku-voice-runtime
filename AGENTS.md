# Coding-agent maintenance guide

This is the canonical instruction file for coding agents. Human-facing setup belongs in the README and `docs/`; keep this file focused on safe modification.

## Project and runtime

Koehaku Voice Runtime is an open-source local-first realtime voice runtime for AI. It is a self-hosted realtime conversational voice runtime, not an AquesTalk wrapper or a single-model application. Its canonical flow is client PCM/VAD -> STT -> turn handling and Smart Turn -> speculative/normal LLM -> speech chunking -> TTS -> credit-controlled PCM -> browser AudioWorklet. Public interfaces are `GET /health`, `GET /v1/capabilities`, `POST /v1/audio/speech`, `WS /v1/realtime`, and `WS /v1/transcription`.

Provider boundaries are intentional. Whisper uses the existing whisper.cpp implementation. Nemotron uses a dedicated streaming fast path with a persistent sherpa-onnx worker. Shared conversation logic begins after transcript commit. Models, provider executables, proprietary voice assets, and credentials are local dependencies and must not be committed.

## Invariants

- Preserve the Whisper path when changing Nemotron.
- Never silently fall back from Nemotron to Whisper.
- Smart Turn owns the final commit decision; speculation cannot bypass it.
- A speculation timeout may cancel only uncommitted snapshot STT. Once Smart Turn commits that work, the STT call is canonical and may outlive the old speculation deadline; arm a fresh lifetime for speculative LLM work after STT completes. Never couple a committed Whisper request to the timer that guarded its earlier speculative state.
- Partial transcripts are internal hints and are not committed as facts.
- Keep audio ingress bounded and never silently drop accepted audio.
- A single owner controls each Nemotron `OnlineStream`, including decode and finalize.
- Respect session, stream, generation, and epoch ownership. Reject stale events and stale audio.
- Preserve generation-scoped cancellation and playback-aware history.
- Keep `credit-v1` cumulative source-frame accounting and final progress flushes.
- Do not change public API v1, SDK wire events, provider defaults, or unrelated TTS behavior as a side effect of an STT change.
- Apply `SHERPA_ONNX_ORT_DISABLE_SPINNING=1` only to the Nemotron child unless new evidence and review justify broader scope.

## Performance-sensitive areas

Sherpa decode ownership, ONNX Runtime thread behavior, PCM packetization, worker IPC, queue backlog, Smart Turn overlap, speech chunking, native AquesTalk serialization, browser message scheduling, the playback ring buffer, and AudioWorklet allocation can alter latency or correctness. Measure before and after changes under the same fixture and runtime configuration. Never claim improvement from audio drops, truncated transcripts, premature finalization, bypassed Smart Turn, larger timeouts, or hidden metrics.

Phase 3B found ONNX Runtime spinning to be the main long-run contention source. The accepted no-spinning child-process option reduced worker CPU while preserving the 560 ms INT8 CPU2, greedy, auto-language baseline. Dynamic blocking alone, arbitrary priority/affinity, periodic worker restart, queue enlargement, timeout enlargement, and unproven GPU/model-default changes were rejected. See `docs/benchmarks/realtime-performance-investigation-2026-10-04.md`.

## Safe change procedure

1. Read the relevant provider, shared runtime, protocol, tests, and documentation.
2. Identify ownership, cancellation, bounded-queue, and compatibility constraints.
3. Make the smallest coherent change and keep provider-specific work within its provider path.
4. Add a focused regression test when behavior or concurrency changes.
5. Run `go test ./...` and `go build ./cmd/engine`.
6. Run TypeScript, AudioWorklet, Python, and provider tests that cover the changed boundary.
7. For performance work, preserve the benchmark configuration and record raw machine-readable results.
8. Check `git diff --check`, public documentation links, tracked large files, secrets, local paths, and generated artifacts.
9. Report tests that could not run as `SKIP`, with the reason. Never infer a pass.

## Test matrix

- Engine/shared Go change: Go tests and build.
- Public protocol change: Go protocol/transport tests plus TypeScript and Python SDK tests; breaking changes require explicit approval.
- Browser/playback change: TypeScript tests, AudioWorklet tests, realtime browser automation, and manual audible acceptance.
- AquesTalk change: native regression, sequential and parallel stress on a licensed local installation.
- Nemotron change: worker Release build, standalone test, streaming/finalize tests, E2E benchmark, queue/error review, and Whisper preservation check.
- Launcher change: both providers, Ctrl+C cleanup, abnormal-child cleanup, restart, and ports 8765/8766/8080 free.

## Repository map

- `cmd/engine`: executable entry point
- `internal/realtime`, `internal/conversation`: session and generation lifecycle
- `internal/providers`: STT, LLM, turn-detection, TTS, and backchannel providers
- `internal/transport/http`: public HTTP/WebSocket transport
- `internal/audio`, `internal/speech`: PCM flow and speech pipeline
- `sdk/typescript`, `sdk/python`: public SDKs
- `examples`: Browser Voice and API examples
- `scripts/dev.ps1`: local development launcher
- `scripts/benchmarks`, `scripts/diagnostics`: reproducible measurement tools
- `benchmark-results`: selected machine-readable evidence
- `docs/benchmarks`: methodology and technical interpretation
- `third_party/sherpa-onnx`: reproducible Koehaku worker source, CMake snippet, and the opt-in ORT no-spinning patch

## Known boundaries

Physical microphone behavior, audible interruption quality, old-speech resurrection, overlap, and subjective naturalness require manual acceptance. External provider access, AQUEST licensing, and model licenses remain operator responsibilities. First AudioWorklet render is a client timing marker and must not be described as measured audible speaker onset.
