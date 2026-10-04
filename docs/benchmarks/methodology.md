# STT and realtime benchmark methodology

[日本語](methodology.ja.md)

## Scope

Accuracy and STT finalization use the provider-neutral `scripts/benchmarks/stt-accuracy.py` harness over the public `WS /v1/transcription` interface. Full-pipeline measurements use the Browser Voice diagnostic harness and include Smart Turn, speculation, LLM, TTS, WebSocket transport, and AudioWorklet rendering.

## Corpus

`scripts/benchmarks/corpus-manifest.json` contains five Japanese and five English conversational references, including short, medium, long, numeric, and proper-noun samples. `generate-corpus.ps1` creates 16 kHz mono PCM16 WAV files locally with installed Windows voices. Generated audio is ignored by Git and is not distributed. This makes the run reproducible on the test host, but synthetic speech does not represent microphone noise, accents, or room acoustics.

## Accuracy

Each provider receives the same WAV bytes, packet size, pacing, end-of-input signal, and transcript normalization. Japanese is scored by Unicode-normalized character error rate (CER); English is scored by Unicode/case-normalized word error rate (WER). The first repetition is the accuracy sample. The current latency run uses two repetitions per utterance. Provider-specific runtime/model settings are disclosed rather than forced into a false common configuration.

## Latency definitions

- STT finalization latency: client end-of-input send to final transcript receipt.
- Final ASR in the E2E harness: `speech_end` to committed final transcript.
- First render: `speech_end` to the first AudioWorklet render event for the response.
- Audible TTFA: requires physical speaker/microphone measurement and is reported as `SKIP` by headless automation.

Statistics are mean, p50, p95, and max. Raw per-sample or per-turn records are canonical; summaries are derived from them. A zero is valid only when the timeline semantics make it a real measured interval.

## Fairness and limitations

The same machine, OS, corpus, public protocol, packetization, and harness are used. Whisper and Nemotron retain their product configurations because altering decoder/model semantics would compare artificial products. The report lists those differences. Synthetic accuracy and the historical Nemotron full-pipeline run answer different questions and are not combined into one ranking.

Run commands and environment are recorded in the bilingual comparison report. Physical microphone, audible quality, and subjective naturalness remain manual acceptance items.
