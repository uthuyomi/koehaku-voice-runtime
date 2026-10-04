# Commit candidate manifest

This manifest classifies the complete local candidate for the first public **Koehaku Voice Runtime** push. All nonignored modified, deleted, and untracked files belong to one of the groups below. Local models, binaries, credentials, build trees, generated audio, launcher logs, PID files, and scratch resource samplers are excluded.

## Runtime and Public API

- `cmd/`, `internal/audio/`, `internal/conversation/`, `internal/engine/`, `internal/protocol/`, `internal/realtime/`, `internal/speech/`, and `internal/transport/http/`
- Project/module rename, Phase 1–3 realtime corrections, playback accounting, diagnostics, cancellation, and Public API v1 regression coverage
- Public routes and WebSocket semantics remain compatible

## Whisper correction

- `internal/providers/stt/whispercpp/`
- `internal/realtime/speculation.go` and focused tests
- Measurement fields distinguish queueing, WAV preparation, inference, parsing, cancellation, and restart
- A speculation timer cannot cancel committed canonical STT; speculative LLM work receives a fresh lifetime after STT

## Nemotron integration

- `internal/providers/stt/nemotron/` and related realtime/transport tests
- `third_party/sherpa-onnx/`: Koehaku worker source, CMake target snippet, opt-in ORT no-spinning patch, and build instructions
- `scripts/diagnostics/`: soak, browser pipeline, resource sampling, and documented ORT experiment configurations
- Models, worker executables, and the separate experimental microphone tools are excluded

## Other providers and shared conversation behavior

- `internal/providers/llm/`, `internal/providers/turndetection/`, `internal/providers/backchannel/`, and `internal/providers/tts/`
- Smart Turn timing, LLM instrumentation, AquesTalk correctness/serialization, diagnostic privacy, and regression tests

## SDKs and compatibility

- `sdk/typescript/`: canonical `@koehaku-voice/client`, browser/AudioWorklet corrections, package smoke, and compatibility exports
- `sdk/python/`: canonical `koehaku-voice-runtime`, `koehaku_realtime`, and `koehaku`; former `yukkuri_realtime`, `YukkuriClient`, `YukkuriError`, and `yukkuri` remain compatibility aliases
- Protocol schema titles and SDK-generated types remain synchronized

## Launcher and examples

- `scripts/dev.ps1`: Koehaku development launcher, provider preflight, owned-process-tree cleanup, and labeled output
- `examples/`: current Koehaku names and canonical SDK imports while preserving protocol behavior
- `.env.example`: provider configuration examples without secrets or machine-specific paths

## Benchmark infrastructure and evidence

- `scripts/benchmarks/`: corpus manifest, local synthesis, accuracy harness, and instructions
- `benchmark-results/2026-10-04/`: selected Phase 1–3B, Whisper failure/fix, final 30/100-turn, Nemotron regression, accuracy, and resource evidence
- `docs/benchmarks/`, `docs/en/benchmarks/`, and `docs/ja/benchmarks/`: methodology, engineering history, and bilingual interpretation
- The original Whisper 19/30 failure and the successful 30/100-turn runs are retained. Two uncited runs made with unintended fixed-Japanese configuration were removed as invalid duplicate release evidence.

## Human and AI documentation

- `README.md`, `README.ja.md`, current `docs/`, `SECURITY.md`, and this manifest
- `AGENTS.md` is the canonical coding-agent maintenance guide; `docs/ja/ai-maintenance-guide.md` is its Japanese companion
- Obsolete corrupted `docs/public-api-finishing.md` is intentionally deleted; the current API documentation replaces it

## Licensing and repository hygiene

- `LICENSE` remains the project MIT license
- `THIRD_PARTY_NOTICES.md` separates project code from AQUEST assets, sherpa-onnx/ONNX Runtime, Nemotron, whisper.cpp/models, Smart Turn, and external services
- `.gitignore`, `scripts/release_check.py`, and its tests exclude or detect local secrets, proprietary assets, models, binaries, generated audio, build/package output, and benchmark scratch
- Empty tracked `log.txt` is intentionally deleted

## Final review rule

`git add -A` is safe after reviewing this manifest and the final diff: every remaining nonignored path is intended for the candidate. Before committing, run `git diff --cached --check` and inspect `git diff --cached --stat`.
