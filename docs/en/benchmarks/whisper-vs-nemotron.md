# Whisper vs Nemotron benchmark

[日本語](../../ja/benchmarks/whisper-vs-nemotron.md) | [Methodology](../../benchmarks/methodology.md) | [Raw results](../../../benchmark-results/2026-10-04/)

## Environment

Tests ran on Windows 11, Intel Core i7-1260P, 31.7 GiB RAM. Both providers received the same 16 kHz mono PCM fixtures through Public API v1. Nemotron used the 560 ms INT8 model, CPU, 2 threads, greedy search, language `auto`, persistent worker, and ORT no-spinning. Whisper used whisper.cpp `small`, CPU, 4 threads, best-of 5, beam size 5, language `auto`, and a persistent server.

## Accuracy

| Metric | Whisper | Nemotron |
|---|---:|---:|
| Japanese CER | 11.96% | 2.17% |
| English WER | 10.00% | 7.50% |
| Request/final mean | 22.275 s | 4.287 s |
| Request/final p50 | 20.734 s | 3.820 s |
| Request/final p95 | 30.085 s | 6.670 s |
| Request/final max | 31.550 s | 7.321 s |

The corpus has five locally synthesized references per language. It is a regression comparison, not a broad real-world accuracy claim.

## Whisper blocker and correction

The original Whisper E2E run stopped at 19/30. A 45-second speculation timer began before snapshot STT. Legitimate CPU inference could exceed that lifetime; after Smart Turn committed the turn, the expired timer still cancelled the canonical HTTP inference. Cancellation terminated the persistent server, and the normal path then reloaded the model and retranscribed the same audio. Repetition produced the observed 45-second cancellation, model reload, and duplicate-inference cascade.

The timer now protects only uncommitted speculative work while snapshot STT is pending. Once Smart Turn commits that work, expiry no longer cancels canonical STT. After STT returns, a fresh lifetime is armed for the speculative LLM request. Public API, Smart Turn semantics, Whisper decoding settings, and Nemotron streaming ownership are unchanged. Focused tests cover a committed STT request that exceeds the speculation timeout and assert one STT call.

## Full realtime pipeline

| Metric | Whisper 30-turn | Whisper 100-turn | Nemotron Phase 3B 30-turn | Nemotron Phase 3B 100-turn | Nemotron final regression 30-turn |
|---|---:|---:|---:|---:|---:|
| Completed | 30/30 | 100/100 | 30/30 | 100/100 | 30/30 |
| Final ASR mean | 27.668 s | 35.456 s | 0.947 s | 1.019 s | 1.617 s |
| Final ASR p50 | 20.886 s | 34.947 s | 0.834 s | 0.915 s | 1.116 s |
| Final ASR p95 | 44.606 s | 47.159 s | 1.273 s | 1.813 s | 3.846 s |
| Final ASR max | 70.660 s | 78.007 s | 1.992 s | 3.317 s | 5.433 s |
| First render mean | 28.648 s | 36.686 s | 1.725 s | 1.831 s | 2.519 s |
| First render p50 | 21.687 s | 36.544 s | 1.457 s | 1.594 s | 1.860 s |
| First render p95 | 45.627 s | 48.473 s | 2.940 s | 3.327 s | 5.997 s |
| First render max | 72.491 s | 80.968 s | 3.277 s | 4.680 s | 6.613 s |
| AudioWorklet underruns | 0 | 0 | 0 | 0 | 0 |
| External turn failures | 0 | 0 | 0 | 0 | 0 |

Whisper 100-turn first/middle/last groups had Final ASR means of 32.487/37.659/34.017 seconds and first-render means of 33.645/38.946/35.206 seconds. There was no turn-number growth. Speculation produced 99 promotions and one mismatch. One Smart Turn continue/resume cancellation intentionally invalidated an in-flight speculative snapshot, causing one server restart; the canonical turn recovered. There were no speculation-timeout cancellations or stopped turns.

The final Nemotron regression remained functionally correct but was slower than the preserved Phase 3B result. Because the production change is confined to snapshot speculation used by Whisper and does not enter the single-owner Nemotron streaming path, the run is reported as host jitter evidence rather than substituted for the Phase 3B baseline. It does not support a claim that Nemotron latency improved.

## Resource observations

Across 174 samples in the Whisper 100-turn run, whisper-server working set changed from 765.4 to 771.7 MiB, private bytes from 1255.7 to 1263.2 MiB, with maxima of 65 threads and 194 handles. Engine working set changed from 64.2 to 40.2 MiB and private bytes from 57.8 to 64.6 MiB. No linear resource growth was observed.

## Limitations

First AudioWorklet render is a browser timing marker, not audible speaker onset. Physical microphone input, room noise, accents, barge-in feel, and output naturalness remain manual acceptance items. LLM/TTS variability contributes to full-pipeline timing. Whisper CPU latency remains high even though the correctness blocker is fixed.

## Raw data

- [Whisper auto accuracy](../../../benchmark-results/2026-10-04/public-whisper-accuracy-koehaku.json)
- [Original incomplete Whisper run](../../../benchmark-results/2026-10-04/public-whisper-e2e-partial.json)
- [Fixed Whisper 30-turn](../../../benchmark-results/2026-10-04/public-whisper-auto-e2e-30.json)
- [Fixed Whisper 100-turn](../../../benchmark-results/2026-10-04/public-whisper-auto-e2e-100.json)
- [Whisper 100-turn resources](../../../benchmark-results/2026-10-04/public-whisper-auto-e2e-100-resources.csv)
- [Whisper final diagnostic summary](../../../benchmark-results/2026-10-04/public-whisper-final-summary.json)
- [Nemotron accuracy](../../../benchmark-results/2026-10-04/public-nemotron-accuracy.json)
- [Nemotron Phase 3B 30-turn](../../../benchmark-results/2026-10-04/phase3b-e2e-30.json)
- [Nemotron Phase 3B 100-turn](../../../benchmark-results/2026-10-04/phase3b-final-e2e-100.json)
- [Nemotron final regression](../../../benchmark-results/2026-10-04/public-nemotron-final-e2e-30.json)
