# Realtime conversation performance investigation (2026-10-04)

## 1. Executive summary

The ten-turn Browser/AudioWorklet run reproduced both turn-over-turn latency and audible starvation. No production tuning was applied. The run used Nemotron 560 ms INT8, CPU, two threads, automatic language selection, Smart Turn, speculation, AquesTalk, credit-v1, the TypeScript SDK, Chrome's real AudioWorklet and the existing 30 ms/10 ms worklet watermarks.

Four independent findings explain the unusable behavior.

1. **P0: Nemotron cannot keep up with exact real-time PCM under the integrated CPU load.** `speech_end -> final ASR` was 2.383 s on turn 1 and 7.090-9.939 s thereafter. The same persistent worker, run alone for ten utterances, had stable first partials (1.253-1.514 s) and finalization (0.460-1.177 s), with no turn-number growth. This locates the degradation in integrated CPU scheduling/backlog, rather than retained `OnlineStream` state.
2. **P0: AquesTalk sometimes fails and terminates output.** Four of thirteen observed integrated turns ended with `generation_failed`; the newly exposed internal error was `AquesTalk voice "f1" synthesis failed with error code 0`. This is a direct cause of cut-off speech.
3. **P0: PCM delivery starves the AudioWorklet.** Completed turns had 11-35 underruns. One measured response generated 10.109 s of PCM but accumulated 29.782 s of credit wait. Native AquesTalk was fast (1.763-3.635 ms in the measured chunks); the delay was after synthesis, in paced WebSocket delivery/credit feedback and synchronous logging.
4. **P1: playback feedback is excessively chatty and every event is synchronously logged.** A completed response produced 226-469 worklet reports and 125-240 server events. `BrowserAudioPlayer` calls `ackPlayed()` for both worklet credit and progress reports; `ackPlayed()` emits both `playback.credit` and `playback.progress`. The server logs every incoming event. A three-turn run emitted event IDs through 2105. Contention on the global Go logger was directly visible: a second AquesTalk call spent 1.412 s end-to-end while its native call took 1.763 ms.

The primary user-perceived TTFA is dominated by final ASR. Normal LLM TTFT after final ASR was 0.491-1.732 s, TTS was requested 15-220 ms after the first LLM delta, Browser PCM arrived 1.8-3.1 ms after the TTS chunk event, and the first AudioWorklet render followed by 4-56 ms. Smart Turn and LLM matter, but they do not explain the turn-2 jump.

## 2. Turn 1-10 latency table

All values are milliseconds relative to `speech_end`. A dash means output was terminated by the AquesTalk failure before playback completion.

| Turn | Final ASR | LLM first delta | First TTS chunk | Browser PCM | First render (TTFA) | Playback complete | Playback duration | Underruns | Result |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 1 | 2,383 | 4,115 | 4,335 | 4,337 | 4,387 | - | - | - | AquesTalk failure |
| 2 | 9,491 | 11,128 | 11,313 | 11,315 | 11,360 | - | - | - | AquesTalk failure |
| 3 | 9,939 | 11,280 | 11,469 | 11,472 | 11,528 | - | - | - | AquesTalk failure |
| 4 | 9,539 | 11,272 | 11,453 | 11,456 | 11,460 | 53,509 | 42,059 | 35 | completed |
| 5 | 7,967 | 8,856 | 9,037 | 9,040 | 9,086 | 32,675 | 23,629 | 18 | completed |
| 6 | 7,976 | 8,795 | 8,810 | 8,812 | 8,816 | 30,937 | 22,123 | 18 | completed |
| 7 | 8,017 | 8,658 | 8,840 | 8,843 | 8,887 | 24,867 | 16,021 | 12 | completed |
| 8 | 8,483 | 8,974 | 9,194 | 9,196 | 9,239 | 25,709 | 16,509 | 11 | completed |
| 9 | 7,295 | 7,954 | 8,165 | 8,167 | 8,219 | 24,739 | 16,560 | 11 | completed |
| 10 | 7,090 | 7,912 | 8,056 | 8,058 | 8,102 | 33,872 | 25,811 | 21 | completed |

The complete table is in `../../benchmark-results/2026-10-04/nemotron-full-pipeline-10-turns-2026-10-04.csv`.

## 3. End-to-end critical path

The instrumented single-turn run separated the first response as follows:

- VAD handling/freeze/speculative request: approximately 1 ms
- Smart Turn: 214.751 ms; connection 1.981 ms, first byte 213.492 ms
- intentional endpoint minimum plus Smart Turn to finalize request: 516.44 ms from speech end
- Nemotron provider finalize: 1,448.69 ms
- final transcript commit: 1,965.13 ms
- normal OpenAI request: reused connection, response headers at 362.440 ms, first text delta at 1,976.258 ms
- first TTS start: 4,085.15 ms from speech end
- first AquesTalk chunk: 6.437 s of PCM generated in 4.722 ms; native synthesis was 3.635 ms
- first PCM sent: 4,095.89 ms
- Browser first PCM: 4,098.9 ms
- AudioWorklet first render: 4,109.3 ms

Transport from server TTS start through first render was about 24 ms in this run. The large initial delay was upstream in final ASR and LLM. The later audible gaps were downstream in sustained delivery.

## 4. Why turn 2 and later are slower

**WHAT:** final ASR rises from roughly 1.8-2.4 s to 5.5-9.9 s.

**WHERE:** accepted PCM waits for Nemotron decoding before the finalize command can be processed. The final two `DecodeStream` calls are not the whole delay; the provider/worker FIFO must first drain preceding PCM.

**WHY:** the worker uses two CPU inference threads and is CPU-bound. During the resource sample it consumed 310.62 CPU seconds over 261 seconds of wall time (about 119% of one logical CPU on average, including non-ASR playback periods). Integrated Chrome, Smart Turn, launcher logging and Engine work compete with it. Exact real-time browser PCM continues arriving while decoding falls behind.

**WHEN:** the first integrated turn was relatively fast. The second turn jumped immediately. Turns 2-10 remained slow rather than growing monotonically.

**GROWTH:** there is no evidence of per-turn `OnlineStream` accumulation. The worker-only ten-utterance test stayed flat; working set rose only 2.2 MiB, handles stayed at 151, and streams were destroyed after finalization. A five-second pause after playback reduced turns 2/3 to 5.465/6.538 s, but did not restore turn-1 performance. This supports CPU/scheduler/thermal recovery rather than stale playback state as the dominant mechanism.

## 5. Root cause of audio cut-outs

There are two observed mechanisms.

1. AquesTalk returned a null result with error code 0 on a later speech chunk. The Engine converted this to `generation_failed` and cancelled output. This produced three failures in the ten-turn baseline and one failure in the three-turn five-second-gap run.
2. Successful generations still starved. They recorded 11-35 worklet underruns. The worklet's ring reached its configured 2,000 ms network window, then repeatedly drained to zero while generation was still active. This is PCM arrival starvation, not a resampler arithmetic defect.

The standalone AudioWorklet suite passed all 14 cases, including a ten-minute stream with fixed storage/no drift, exact packet-boundary equivalence, underrun/rebuffer behavior and final drain. The TypeScript SDK suite passed all 20 cases after the live server was stopped. This excludes the ring/resampler implementation as the direct source of the gaps.

## 6. Nemotron analysis

Worker-only ten-utterance results:

- first useful partial: 1.253-1.514 s, no turn trend
- finalize client time: 0.460-1.177 s
- finalize queue wait: 0.018-242.168 ms
- remaining final decode: 0.458-1.160 s, exactly two calls per turn
- `DecodeStream` count: 16 per utterance
- working set: 758.9 -> 761.1 MiB
- private bytes: 781.4 -> 783.5 MiB
- handles: 151 -> 151
- threads: 5 -> 7, a one-time increase rather than linear growth

This test used the same model, CPU provider, two threads, greedy search and automatic language setting. The model/settings were not changed. The evidence rejects stream leakage and points to inability to maintain real-time throughput when the whole application is active.

## 7. Smart Turn analysis

Ten isolated POST requests took 68.877-159.717 ms, mean 103.678 ms. The integrated traced request took 214.751 ms, of which loopback connection setup was only 1.981 ms and the first response byte took 213.492 ms. The previously observed 530.67 ms is therefore sidecar inference/scheduling contention, not HTTP serialization or loopback transport. It is a P1 contributor, not the turn-2 root cause. The sidecar remained stable at approximately 127.5-129.2 MiB working set and 225 handles.

## 8. Speculation analysis

Across ten turns:

- attempts: 10
- promotions: 9
- transcript mismatches: 1
- cancelled/wasted request: 1
- mismatch wasted wall time: 2,378 ms
- promotion-reported savings: 469-688 ms per promoted turn

Speculation was beneficial for nine turns and wasteful for one. The normal request reused the existing HTTP connection in the traced mismatch case. There is no evidence that speculation caused the turn-over-turn ASR degradation. Its mismatch cost is real but secondary.

## 9. LLM analysis

In the traced mismatch turn, the speculative request opened a connection in 243.841 ms and received headers at 1,268.604 ms. The normal request reused a connection, received headers at 362.440 ms, produced its first text delta at 1,976.258 ms and completed at 2,383.377 ms. Across the ten-turn run, final-ASR-to-first-delta was 0.491-1.732 s and did not grow with turn number. Conversation growth and connection pool pressure are therefore not responsible for the observed second-turn jump.

## 10. TTS analysis

Measured AquesTalk work was normally far faster than real time:

- chunk 0: 121 input bytes, 150 symbol bytes, 51,496 PCM frames / 6.437 s; Kanji2Koe 1.086 ms, native synthesis 3.635 ms, total 4.722 ms
- chunk 1: 75 input bytes, 96 symbol bytes, 29,376 PCM frames / 3.672 s; native synthesis 1.763 ms

The second call nevertheless showed 1,412 ms provider wall time and 2,906 ms through decode/logging. The difference occurred around synchronous diagnostic/log writes while hundreds of playback events were being logged. Native TTS speed is not the sustained-playback bottleneck.

The sporadic AquesTalk error-code-0 failure remains a separate P0 correctness issue. Exact failing text was deliberately not placed in logs. A correction phase should add a privacy-controlled failing-input replay artifact before changing the provider.

## 11. Playback and credit-v1 analysis

`reserved_source_frames` is cumulative frames authorized for the generation. It is expected to exceed `capacity_source_frames`; the actual invariant is `reserved - played <= capacity`. It does not itself prove over-reservation.

The observed problem is pacing:

- a measured generation: capacity 16,000 frames, reserved 80,872 frames, cumulative credit wait 29,782 ms, 42 waits
- completed baseline generations: 226-469 worklet reports each
- Worklet reports caused two outbound control events through `ackPlayed()`
- one three-turn run reached related event sequence 2105
- synchronous `realtime event received` logging records every credit/progress event

The event volume is not a main-thread computational burden in isolation, but the launcher/console logging path magnifies it and blocks goroutines sharing Go's global logger. This also explains why diagnostic logging itself materially perturbed TTS wall-time measurements.

## 12. Browser and AudioWorklet analysis

Browser PCM to first render was 4-56 ms. Initial playback starts correctly. Sustained playback fails because data arrives in bursts: successful responses recorded 11-35 underruns while `max_buffered_ms` reached 2,000 ms. The worklet reports exact source progress and drains to the exact final frame in tests. No overflow occurred.

The headless Chrome harness used the real SDK WebSocket parser and the real AudioWorklet. It retained all diagnostic events, so its renderer memory growth cannot be used as production leak evidence. Audio timing and underrun counts remain valid.

## 13. CPU, memory and resource analysis

| Process | CPU over 261 s | Working set first -> last | Threads first -> last | Handles first -> last | Finding |
|---|---:|---:|---:|---:|---|
| Nemotron worker | 310.62 s | 758.9 -> 761.1 MiB | 5 -> 7 | 151 -> 151 | CPU-bound, no linear memory/handle leak |
| Smart Turn Python | 3.91 s | 127.5 -> 129.2 MiB | 16 -> 18 | 225 -> 225 | stable |
| Engine point sample | 2.30 s total | 35.1 MiB | 17 | 378 | low CPU; sampler pattern omitted it from the time series |
| diagnostic Chrome renderer | 24.30 s | 96.9 -> 199.6 MiB, peak 317.8 | 31 -> 31 | 402 -> 395 | harness retained 2.1 MiB of raw metrics; not leak evidence |
| launcher PowerShell | 56.14 s | 92.1 -> 97.7 MiB | 30 -> 13 | 686 -> 651 | significant cost from relaying very large logs |

No monotonic worker, sidecar or handle leak was found. Engine goroutine and GC-pause time series were not available in this build, so those remain P3 unknowns rather than asserted causes.

## 14. Controlled A/B results

| Test | Result |
|---|---|
| Full pipeline, ten turns, next turn 500 ms after playback completion | final ASR 2.383 s on turn 1, 7.090-9.939 s afterward; 11-35 underruns |
| Full pipeline, three turns, five seconds after playback completion | final ASR 1.758, 5.465, 6.538 s; waiting helps but does not restore baseline |
| Nemotron worker only, ten utterances | no degradation; partial and finalize stable |
| Smart Turn only, ten requests | mean 103.678 ms; no degradation |
| Real AudioWorklet full pipeline | PCM arrival starvation reproduced |
| AudioWorklet deterministic suite | 14/14 pass, including ten-minute no-drift run |
| SDK suite | 20/20 pass after live-server interference was removed |
| Go suite | `go test ./...` passes |

The decisive comparison is worker-only versus full pipeline. The decoder does not retain per-turn state, but loses real-time throughput under integrated scheduling/load. Playback remains defective even when ASR is no longer active, due to delivery/feedback/logging and sporadic AquesTalk failure.

## 15. Cause list by priority

### P0

1. Integrated Nemotron decode backlog makes final ASR 7-10 s on later turns.
2. AquesTalk returns error code 0 on some later chunks and aborts the generation.
3. Server-to-Worklet PCM starvation causes 11-35 audible underruns per completed response.

### P1

1. Credit/progress amplification plus unconditional per-event logging creates thousands of events and measurable logger contention.
2. Smart Turn expands from about 104 ms isolated to 215-531 ms under integrated load.
3. A mismatched speculative request wastes one LLM request and about 2.38 s, although promotion is beneficial in 9/10 turns.

### P2

1. OpenAI request-phase observability should remain; connection reuse works and normal TTFT is not growing.
2. Aggregate playback metrics should distinguish cumulative reserved frames from outstanding frames.
3. Resource sampling should include Engine goroutines/GC and per-core scheduler data.

### P3 / disproved or unsupported

1. `reserved_source_frames > capacity_source_frames` is not itself a bug; reserved is cumulative.
2. AudioWorklet resampler drift/ring leakage was not reproduced.
3. Nemotron `OnlineStream`, worker state, handles and memory did not accumulate over ten isolated turns.
4. Conversation context growth did not correlate with LLM TTFT in this run.

## 16. Expected improvement if each cause is fixed

These are bounded projections from measured stages, not implemented results.

- Restoring integrated Nemotron throughput to the worker-only range would reduce later-turn TTFA by roughly 5.9-8.8 s.
- Removing AquesTalk error-code-0 failures would eliminate the observed 4/13 hard output cancellations.
- Delivering generated PCM continuously would reduce underruns from 11-35 toward zero and make playback duration approach PCM duration; it does not materially change first PCM TTFA.
- Coalescing feedback/logging would remove thousands of events per session and the measured 1-3 s logging stalls around later TTS chunks.
- Smart Turn returning to isolated performance would save approximately 0.1-0.4 s.
- Speculation already saves about 0.5-0.7 s on matching turns; avoiding false speculation would save one wasted request on the mismatch turn.

## 17. Files and functions implicated in a correction phase

- `sherpa-onnx/csrc/koehaku-streaming-asr-worker.cc`: PCM decode throughput, command backlog and per-stream destruction evidence
- `internal/providers/stt/nemotron/provider.go`: provider write queue and finalize enqueue/receive timing
- `internal/realtime/streaming_stt.go`: speech-end/final-ASR critical path
- `internal/providers/tts/aquestalk/provider_windows.go`, `Provider.Synthesize`: error-code-0 reproduction and failing-input artifact
- `internal/transport/http/realtime.go`, `synthesizeSpeechChunk` and audio send loop: serialization, credit waits and per-event logging
- `internal/audio/flow.go`, `FlowController.Reserve`: wait accounting and outstanding-window diagnostics
- `sdk/typescript/src/browser.ts`, `BrowserAudioPlayer`: feedback forwarding
- `sdk/typescript/src/session.ts`, `ackPlayed`: paired credit/progress emission
- `sdk/typescript/src/audio-worklet.js`, `report` and `process`: progress cadence and underrun evidence
- `scripts/dev.ps1`: synchronous log relay cost
- `internal/providers/turndetection/smartturn/provider.go`: inference/HTTP breakdown
- `internal/providers/llm/openai/provider.go`: request/connection/first-byte/first-delta metrics

## 18. Recommended correction order

1. Reproduce and fix the AquesTalk error-code-0 failure with the exact failing chunk captured behind an explicit diagnostic flag.
2. Make Nemotron meet real-time throughput in the integrated CPU/scheduler environment while preserving the model, CPU2, decoder and public behavior.
3. Remove PCM starvation by correcting feedback cadence/transport scheduling; verify zero underruns with the same AudioWorklet harness.
4. Reduce or aggregate per-credit/progress logging and verify that the launcher no longer perturbs runtime scheduling.
5. Re-run ten turns for playback-complete, playback-overlap and five-second-gap boundaries, then compare per-core CPU and worker backlog.
6. Only after P0/P1 results are stable, evaluate speculation mismatch policy and Smart Turn optimization.

No performance correction was implemented in this investigation. Changes are limited to instrumentation and the diagnostic Browser harness.

## Phase 2: realtime data plane correction

### Root cause and architecture decision

The integrated worker was measuring `accepted_samples` as if it were decoded audio. Its queue metric covered only PCM commands still waiting in the command deque, so audio already passed to `AcceptWaveform()` but still waiting for `DecodeStream()` was invisible. In addition, each 20 ms transport packet was handled as a separate command. Under integrated CPU load this produced avoidable owner-thread wakeups and made finalize wait behind many packet commands.

The corrected worker retains one logical owner for every `OnlineStream`. The socket reader only validates and enqueues into the existing bounded ten-second ingress queue. The decode owner coalesces adjacent, already-queued PCM commands for the same stream without a batching timer and without crossing start/finalize/cancel ordering. That owner alone calls `AcceptWaveform`, `IsReady`, `DecodeStream`, `InputFinished`, `GetResult`, and destroys the stream. Finalize snapshots the true speech-end backlog, then preserves the required `InputFinished` and remaining `IsReady` decode loop.

`processed_samples` now comes from `OnlineStream::GetNumProcessedFrames()` using the model's 10 ms frame shift and is capped by accepted input. Partial backlog is `accepted - processed` plus PCM still queued for that stream. The final response reports the backlog captured when the finalize command arrived. The audio timeline is not reset. Performance output now includes accepted/processed samples, queue high watermark, PCM command and coalesced `AcceptWaveform` counts, decode p50/p95/max, scheduling gaps, finalize queue wait, remaining decode time, and end-to-end throughput.

The protocol framing and public API did not change. The model remains Nemotron 3.5 Streaming ASR 0.6B 560 ms INT8 on CPU with two inference threads, greedy search, automatic language detection, and a persistent worker. Whisper source and behavior were not changed. Phase 1's two-second credit window, cumulative source-frame accounting, 100 ms progress reports, and final progress flush remain in place.

SharedArrayBuffer was not adopted: the existing MessagePort/ring path remained stable in the 10-turn correction run and changing the browser deployment/isolation contract was not justified. A Windows process-priority A/B reduced mean final-ASR from 1.385 s to 1.215 s, but did not materially change the measured streaming throughput ratio and included external LLM variance. The production worker therefore remains at Normal priority; no affinity or thread-priority override was added.

### Controlled results

The same 8.162-second Japanese fixture and the same full-pipeline configuration were used.

| Run | Turns | final ASR mean / p50 / p95 / max | first render mean / p50 / p95 / max | Hard failures | Underruns |
|---|---:|---|---|---:|---:|
| Phase 1 baseline | 10 | 3.859 s / n/a / n/a / 7.910 s | 5.754 s / n/a / n/a / 10.453 s | 0 | 0 |
| Phase 2 corrected | 10 | 1.385 / 1.210 / 1.495 / 2.647 s | 3.424 / 3.230 / 4.077 / 5.183 s | 0 | 0 |
| Phase 2 corrected | 30 | 1.128 / 1.063 / 1.273 / 1.783 s | 3.118 / 3.117 / 3.907 / 4.108 s | 0 | 2 |

The 30-turn first ten final-ASR mean was 1.099 s and the last ten mean was 1.090 s, so there was no turn-number degradation. All 30 generations completed; worker crash, provider error, stale-stream error, silent drop, queue overflow, AquesTalk hard failure, and playback completion failure were zero. Two isolated AudioWorklet underruns were reported on turns 24 and 27. This misses the strict zero-underrun gate and remains a Phase 2 limitation rather than being hidden by a larger buffer or metric change.

Typical integrated turns ended speech with 5,149 queued samples (322 ms), versus the multi-second invisible backlog seen before correction. A representative corrected turn processed 130,589 samples, used 16 decode calls, and completed its remaining two decode calls in about 0.60-0.74 s. End-to-end streaming ratios were commonly 0.83-0.85 because the measurement includes capture time plus final padding/drain. The Engine+Nemotron-only one-shot test processed the same 8.162 s input at ratios logged between 1.45 and 1.80, demonstrating that raw decode throughput exceeds realtime when the audio is immediately available.

Ten Engine+Nemotron-only transcription requests all succeeded in 4.37-5.97 s, with identical output byte counts. The full 30-turn resource interval was 470 s: worker CPU increased by 542.7 s, Engine CPU by 5.0 s, and Smart Turn CPU by 9.9 s. Worker working set stayed at about 759 MiB and its thread count returned from seven to five; there was no per-turn memory growth.

The raw artifacts are:

- `../../benchmark-results/2026-10-04/phase2-batched-10-turns-2026-10-04.json`
- `../../benchmark-results/2026-10-04/phase2-above-normal-10-turns-2026-10-04.json`
- `../../benchmark-results/2026-10-04/phase2-30-turns-2026-10-04.json`
- `../../benchmark-results/2026-10-04/phase2-worker-path-10-utterances-2026-10-04.json`
- `../../benchmark-results/2026-10-04/phase2-resource-snapshot-2026-10-04.csv`
- `../../benchmark-results/2026-10-04/phase2-five-second-gap-2026-10-04.json`

Japanese automatic-language recognition was exercised by every controlled run. A temporary Windows Zira 16 kHz English fixture also completed through the unchanged `language=auto` transcription path in 3.063 s. The transcript was kept outside the repository. The 500 ms post-playback boundary was covered by the 10- and 30-turn runs. A separate three-turn five-second-gap run completed with final-ASR times of 1.151, 1.869, and 1.248 s and zero underruns. Real-microphone barge-in remains a manual follow-up check; the production interruption and speculation policies were left unchanged.

### Verification

- Nemotron worker Release build: passed.
- `go test ./...`: passed.
- `go build ./cmd/engine`: passed.
- TypeScript SDK build and 20 tests, including the AudioWorklet test: passed.
- Launcher Ctrl+C cleanup: Engine, Smart Turn, Browser server, and worker exited; ports 8765, 8766, and 8080 were free.
- `git diff --check`: passed in both repositories.

The remaining bottleneck is CPU inference variability under integrated load: individual 560 ms model decode calls still vary by hundreds of milliseconds. Phase 3 can evaluate speculation and Smart Turn latency, other runtimes/models, and GPU execution. Those changes were not included in Phase 2.

## Phase 1 correction results

Phase 1 was run with the same prerecorded input, real Engine/Smart Turn/OpenAI/AquesTalk providers, Nemotron 560 ms INT8 on CPU with two threads and automatic language detection, and a real Chrome AudioWorklet.

The observed AquesTalk failure was an LLM semantic chunk containing three bytes of whitespace. AqKanji2Koe returned an empty symbol string; passing that empty string to `AquesTalk_Synthe_Utf8` returned NULL while leaving its output error integer at zero. The provider now reports the empty conversion as `tts.ErrNoSpeech`, and the speech pipeline omits only that non-pronounceable chunk. It does not normalize ordinary text or retry native synthesis. The native provider is also serialized because an unfixed 8-goroutine reproduction against its shared AqKanji2Koe/AquesTalk state crashed with a Windows access violation. After serialization, 100 sequential calls and 800 calls from eight concurrent goroutines passed.

Failed native calls can be captured only when `KOEHAKU_TTS_DIAGNOSTIC_DIR` is explicitly set. The former `YUKKURI_TTS_DIAGNOSTIC_DIR` remains a compatibility alias. The private JSON artifact includes original and converted text, voice, speed, byte lengths, UTC timestamp, generation, sequence, and native error code. Normal logs contain none of those text fields.

The browser feedback path no longer sends credit for every received PCM packet. Rendered progress is reported at a 100 ms frame threshold, credit-v1 receives one cumulative credit snapshot for that progress, and a final progress snapshot is flushed on completion, pause, cancellation, generation replacement, player/session close, and the legacy non-credit path. The two-second credit window remains unchanged. Engine logging suppresses synchronous per-event lines for `playback.credit` and `playback.progress`; the existing per-chunk audio-flow line now includes aggregate `credit_updates`.

| Metric | Baseline 10 turns | Phase 1 10 turns |
|---|---:|---:|
| AquesTalk hard failures | 3/10 (4/13 including follow-up) | 0/10 |
| Mean speech end to final ASR | 7,818 ms | 3,859 ms |
| Mean speech end to first LLM delta | 8,894 ms | 5,409 ms |
| Mean speech end to first render | 9,108 ms | 5,754 ms |
| AudioWorklet reports | 2,741 | 877 |
| Incoming realtime events | 1,474 | about 900 (877 feedback plus session/input control) |
| Underruns on successful generations | 11-35 | 0 on all 10 |
| Launcher PowerShell CPU | 56.14 s over 261 s | 15.64 s over approximately 265 s |

The Phase 1 first-render values by turn were 10,453, 8,328, 7,186, 7,023, 3,215, 5,037, 4,268, 4,148, 4,415, and 3,467 ms. Playback completed on all turns at 20,212, 18,008, 16,286, 17,083, 10,455, 12,437, 11,508, 13,547, 11,825, and 10,397 ms. The raw result is `../../benchmark-results/2026-10-04/phase1-after-10-turns-2026-10-04.json`. The harness recorded its ten completed turns before reporting a harness-only `client.close is not a function` cleanup error; that obsolete call has been removed.

These results meet the Phase 1 correctness, feedback, logging, and measurement gates. The Whisper launcher process check could not run because this workstation has no runnable `whisper-server.exe`; its preflight stopped before starting any process, while the Whisper Go provider tests passed. The results do not establish that integrated Nemotron throughput is fixed: final-ASR latency still ranged from 1.26 to 7.91 seconds, and first-render latency ranged from 3.22 to 10.45 seconds. PCM scheduling and integrated Nemotron throughput remain the Phase 2 work.

## Phase 3: final stabilization, latency analysis, and endurance

### 1. Final verdict

**B. REMAINING BLOCKER.** The remaining AudioWorklet underrun was corrected and the 100-turn run completed with zero underruns, hard failures, provider errors, queue overflows, worker crashes, and playback-completion failures. The run nevertheless showed late-run Nemotron inference jitter and final-ASR degradation, and hardware-dependent real-microphone barge-in/resume acceptance could not be executed in this automated environment. The Phase 3 completion gate is therefore not claimed.

### 2. Executive summary

Phase 2 turns 24 and 27 each began Worklet rendering after receiving only one 256-frame, 8 kHz packet (32 ms). The next PCM delivery did not reach the Worklet for 269.9 and 260.6 ms. The former 30 ms startup threshold therefore guaranteed an immediate underrun on those two generation boundaries. A bounded 300 ms startup watermark now covers the measured initial-delivery jitter without changing the two-second credit window, ring capacity, steady-state rendering, or server behavior. A deterministic regression test reproduces the lone-packet sequence.

The post-correction 100-turn run rotated short, medium, and long Japanese fixtures and an English fixture. All 100 turns completed and AudioWorklet underruns were zero. Final ASR was 1.679 s mean / 1.363 s p50 / 3.654 s p95 / 7.041 s max; first render was 2.526 / 2.098 / 5.024 / 8.585 s. The last 25 turns were slower than the first 25 (final ASR 2.620 versus 1.542 s; render 3.884 versus 2.284 s). Worker memory, handles, and threads were stable after warm-up, while late decode calls became materially slower. This is a performance blocker rather than a resource leak.

### 3. Phase 3 root causes

1. The two Phase 2 underruns were a generation-boundary race: a lone 32 ms packet crossed MessagePort before the remainder of the initial burst, and the 30 ms startup watermark allowed consumption too early.
2. Long-run tail latency is dominated by Nemotron decode/inference jitter. Near stream 100, decode p50/p95/max reached approximately 609/839/850 ms, versus approximately 378/406/409 ms near the beginning. Scheduling gaps were generally small by then, so the measurement implicates the decode call rather than owner-loop queue waiting.
3. Speculation mismatch remains expensive. Exact normalized matching safely promoted 52 turns and rejected 48; the mixed synthetic fixture set intentionally produced revisions. There is insufficient evidence to relax promotion correctness.

### 4. Changed files

Phase 3 changed `sdk/typescript/src/audio-worklet.js` (bounded startup watermark), `sdk/typescript/test/browser.test.mjs` (boundary-race regression), `scripts/diagnostics/perf-browser.js` (fixture rotation and benchmark fields), and `internal/transport/http/realtime.go` (performance-mode Engine resource snapshot). It also adds the Phase 3 raw JSON/CSV artifacts and this report. Earlier Phase 1/2 changes remain described above. No Whisper STT implementation was edited.

### 5. Underrun root cause

| Phase 2 turn | First Worklet input | Gap to next delivery | Buffered frames after next delivery | Result |
|---:|---:|---:|---:|---|
| 24 | 256 frames at 8 kHz (32 ms) | 269.9 ms | 16,255 | one underrun |
| 27 | 256 frames at 8 kHz (32 ms) | 260.6 ms | 16,255 | one underrun |

The other 28 turns had about 16,000 source frames available at first render. Credit was available and the server subsequently delivered a full burst, so this was not credit-window starvation or steady-state server generation starvation. It is classification **I, generation boundary race**, expressed through browser/MessagePort initial-delivery jitter. The underrun report represented an actual empty render and was not a measurement artifact.

### 6. Underrun correction

The default startup buffer changed from 30 ms to a bounded 300 ms (configuration remains clamped, now at 0-500 ms). A 32 ms packet alone no longer starts rendering. Once the initial burst arrives, rendering starts immediately because normal turns already hold roughly two seconds of audio; the 100-turn browser-receive-to-render median remained 101.5 ms. Credit-v1, cumulative accounting, final flush, the two-second credit window, and the 30-second hard ring capacity are unchanged. SharedArrayBuffer/Worker was not introduced because MessagePort was not shown to fail during steady-state rendering.

### 7. Real microphone results

No physical microphone or human listener is available to the automated runner, so the required real-microphone short/long Japanese, English, and mixed-language acceptance remains unexecuted. Browser Voice and the real AudioWorklet path were exercised with 16 kHz prerecorded fixtures; this is not presented as a substitute for the manual gate.

### 8. Barge-in results

The existing Go interruption tests passed and continue to verify generation cancellation and stale-generation isolation. The automated browser run also exercised normal generation cleanup before the next speech. AI-playback-time real-microphone barge-in, audible old-playback stop, and old/new audio non-mixing still require the manual test.

### 9. False endpoint/resume results

Smart Turn and streaming-STT semantics were unchanged and their existing tests passed. A real spoken pause followed by resume, including confirmation that no premature transcript or speculation is committed, was not physically exercised and remains an acceptance blocker.

### 10. JA/EN results

`NEMOTRON_LANGUAGE=auto` was retained. All Japanese and English prerecorded turns completed, and the 100-turn rotation repeatedly crossed JA to EN and EN to JA boundaries without worker/provider errors. Transcript quality and within-one-utterance real-microphone language switching still require human review.

### 11. TTFA critical-path analysis

| Stage, 100 turns | mean | p50 | p95 | max |
|---|---:|---:|---:|---:|
| speech end to final ASR | 1.679 s | 1.363 s | 3.654 s | 7.041 s |
| final ASR to first LLM delta | 0.566 s | 0.439 s | 1.408 s | 3.745 s |
| first LLM delta to first TTS | 0.173 s | 0.134 s | 0.645 s | 1.483 s |
| first TTS to browser PCM | 1.8 ms | 1.6 ms | 2.9 ms | 6.4 ms |
| browser PCM to first render | 106.5 ms | 101.5 ms | 111.0 ms | 598.5 ms |
| speech end to first render | 2.526 s | 2.098 s | 5.024 s | 8.585 s |

Final ASR remains the largest routine stage, with speculation mismatch and occasional LLM delay forming the next largest component. Transport after TTS is negligible. The startup watermark did not impose a 300 ms penalty on normal initial bursts.

### 12. Smart Turn analysis

Smart Turn remained mandatory and its threshold/semantics were not changed. Earlier isolated measurement was approximately 103.7 ms; integrated logs still showed overlap-sensitive hundreds-of-milliseconds behavior. No evidence justified bypass, heuristic replacement, or threshold alteration, so no Smart Turn optimization was adopted.

### 13. Speculation analysis

The 100 turns produced 52 promotions and 48 mismatches. Promoted turns had final-ASR-to-first-delta mean 75 ms and first-render mean 1.477 s; mismatched turns had 1.097 s and 3.663 s respectively. The exact normalized-match policy was retained because the fixtures include meaning-changing revisions and no safe semantic-equivalence proof exists. Partial-revision counts are unavailable through the public event surface used by this harness.

### 14. LLM analysis

Request reuse and provider semantics were retained. First-delta delay did not show the same uniform turn-number pattern as ASR, but mismatch restarts and external-provider variance produced a 3.745 s maximum after final ASR. No model/provider change was made.

### 15. Speech chunker analysis

First useful delta to first TTS was 173 ms mean and 134 ms median. Existing Japanese/English boundaries, whitespace handling, and AquesTalk `ErrNoSpeech` behavior remained intact. The data did not justify smaller fragments and their extra native-call/prosody cost, so chunking was not changed.

### 16. Nemotron jitter analysis

The first 25 turns averaged 1.542 s final ASR; the last 25 averaged 2.620 s. Representative early worker decode p50/p95/max was approximately 378/406/409 ms. Near the final stream it was approximately 609/839/850 ms, while owner scheduling-gap median remained around 2 ms. Speech-end backlog remained bounded and the worker did not leak handles or threads. This points to sustained CPU inference variability, such as power/thermal or competing CPU load, rather than PCM queue accumulation. Per-core ETW evidence was not available, so the precise host-level cause remains open.

### 17. Optional runtime/model/GPU experiments

None were adopted. The 560 ms INT8 CPU2 greedy, automatic-language baseline remains the production default. Phase 2's AboveNormal experiment was not retained because its benefit did not justify scheduling side effects. No compatible 320/160 ms model, alternative runtime, or GPU path was made a default without an equal-input quality/endurance comparison.

### 18. 10-turn benchmark

The mixed-fixture watermark run completed 10/10 with zero underruns and errors. Final ASR was 1.184/1.104/1.310/1.618 s mean/p50/p95/max; first render was 2.264/1.827/3.165/4.575 s. Fixture lengths differ from the Phase 2 single-fixture baseline, so this run validates correctness rather than claiming a direct latency win.

### 19. 30-turn benchmark

The first 30 turns of the final, uninterrupted 100-turn workload completed with zero underruns. Final ASR was 1.528/1.423/2.147/2.696 s; first render was 2.286/1.834/4.113/4.180 s. These are a reproducible subset of the saved 100-turn raw artifact, not a separately restarted process.

### 20. 100-turn benchmark

All 100 turns completed. Fixtures rotated among short Japanese (about 2.2 s), medium Japanese (8.16 s), long Japanese (about 11.5 s), and English (about 6.6 s), with the next turn beginning 250 ms after playback completion. Playback-completion latency was 13.426/10.952/26.380/30.341 s. Harness errors, cancellations, worker errors, provider errors, queue overflow, hard TTS failures, stale streams, and playback completion failures were zero.

### 21. ASR p50/p95/max

Final ASR: **mean 1.679 s, p50 1.363 s, p95 3.654 s, max 7.041 s**. By fixture, means were 1.300 s short JA, 1.680 s medium JA, 1.959 s long JA, and 1.779 s English.

### 22. TTFA p50/p95/max

First AudioWorklet render: **mean 2.526 s, p50 2.098 s, p95 5.024 s, max 8.585 s**. This is better in the center than Phase 2's single-fixture 3.118 s mean/3.117 s p50, but the workloads differ and the long-run tail/degradation fails the non-regression gate.

### 23. Underrun count

**0/10** in the focused post-correction run and **0/100** in endurance, versus 2/30 in Phase 2.

### 24. Failure count

AquesTalk hard failure 0; worker crash 0; provider error 0; silent PCM drop 0; queue overflow 0; stale stream contamination observed 0; playback completion failure 0.

### 25. CPU/resource trends

Worker CPU rose from 6.5 s at start to 1,318 s mid-run and 2,794 s at end, consistent with sustained inference work. Engine working set was 22.7 MiB at start, 37.1 MiB mid-run, and 37.4 MiB at end. Smart Turn was about 106 MiB at start and 135 MiB both mid-run and end. Resource snapshots do not show a monotonic post-warm-up leak.

### 26. Memory/handle/thread/goroutine trends

The worker stabilized near 796 MiB working set / 820 MiB private bytes; threads were 5 at midpoint and end, and handles 151 at both points. Engine threads were 17 at midpoint/end; handles fell from 379 to 289. Smart Turn threads/handles were 16/225 at midpoint and end. Performance-mode Engine snapshots showed about 12-13 goroutines, heap near 2.1-2.3 MiB, and GC pause total about 39 ms by the final generations. GC sawtooth was not treated as a leak.

### 27. Whisper preservation

No Whisper provider/source file was changed. `go test ./...` includes the existing Whisper-facing tests and passed. This workstation still lacks a runnable `whisper-server.exe`, so an end-to-end Whisper launcher run could not be performed; no binary was downloaded or replaced.

### 28. Public API/SDK compatibility

No public event schema, REST route, WebSocket route, TypeScript SDK API, or Python SDK API changed. Browser Voice continued through the real WebSocket and AudioWorklet implementation. Public compatibility suites passed.

### 29. Test results

- `go test ./...`: passed.
- `go build ./cmd/engine`: passed.
- TypeScript SDK/AudioWorklet: 21/21 passed.
- Python SDK: 13/13 passed under Python 3.12 (one test-runner `ResourceWarning`, no failed test).
- AquesTalk: 100 sequential, whitespace `ErrNoSpeech`, and 8 x 100 serialized concurrency passed.
- Nemotron worker Release build: passed.
- Focused 10-turn, 30-turn subset, and 100-turn endurance: completed as above.
- Launcher cleanup: launcher exited and ports 8765, 8766, and 8080 were checked free.
- `git diff --check`: passed.

### 30. Reverted or rejected experiments and why

No huge buffer, PCM drop, timeout extension, Smart Turn bypass, unconditional speculation promotion, Whisper fallback, priority/affinity override, SharedArrayBuffer migration, smaller speech chunks, or model/default change was accepted. Each either lacked causal evidence, risked correctness, or added complexity without measured benefit. Phase 2's AboveNormal A/B remains rejected.

### 31. Remaining limitations

1. Late-run final-ASR latency degrades and decode-call time rises; host power/thermal/per-core scheduling evidence is still needed.
2. Real-microphone barge-in, false endpoint/resume, self-correction, rapid 0/100/250/500 ms turn-taking, and audible JA/EN quality are not automated and remain unverified.
3. Smart Turn integrated stage data and partial-revision history need durable structured capture rather than console-only observations.
4. Whisper end-to-end runtime validation is blocked by the absent local runtime binary.

Recommended next work is a controlled 100-turn repeat with per-core CPU frequency, temperature/power, and ETW scheduling/inference spans, followed by the explicit real-microphone acceptance matrix. This is blocker resolution, not a new feature phase.

### 32. Final architecture

The architecture remains provider-selective: Whisper uses its existing path; Nemotron uses bounded ingress, a single persistent `OnlineStream` owner, continuous decode, and partial/final results; both feed the unchanged conversation, Smart Turn/speculation, LLM, AquesTalk, credit-v1 playback, and Browser AudioWorklet. Phase 3 only makes AudioWorklet generation startup wait for the minimum measured initial burst and adds performance instrumentation.

### 33. Git diff summary

Phase 3 adds a small bounded Worklet startup change, one deterministic regression, benchmark-harness fixture rotation/fields, Engine resource instrumentation gated by `NEMOTRON_PERFORMANCE`, raw benchmark artifacts, and this report. The larger working-tree diff also contains the previously documented Phase 1/2 correctness, telemetry, worker, launcher, and benchmark changes. No dependency or library version was updated, no public protocol was changed, and no Whisper STT implementation was modified.

Raw Phase 3 artifacts:

- `../../benchmark-results/2026-10-04/phase3-watermark-10-turns-2026-10-04.json`
- `../../benchmark-results/2026-10-04/phase3-100-turns-2026-10-04.json`
- `../../benchmark-results/2026-10-04/phase3-endurance-resources-2026-10-04.csv`

## Phase 3B / FINAL: Nemotron long-run decode blocker resolution

### 1. FINAL VERDICT

**AUTOMATED PERFORMANCE / STABILITY COMPLETE 窶・MANUAL MICROPHONE ACCEPTANCE PENDING.**

All automated gates completed, including the final single 100-turn E2E run. Physical microphone behavior remains the only acceptance activity that cannot be performed by this environment. The production baseline remains Nemotron 560 ms INT8, CPU, two threads, greedy search, automatic language selection, persistent worker, and a single `OnlineStream` owner.

### 2. Executive summary

The Phase 3 late-run regression was reproduced without LLM, Smart Turn, TTS, Browser, or playback. During an accelerated worker-only soak, rolling decode p95 crossed 1.30x baseline at utterance 23. Windows processor performance state fell under sustained load, and decode time recovered after idle. ONNX Runtime worker-thread spinning was the controllable source of unnecessary sustained package load. Disabling intra-op and inter-op spinning for the dedicated Nemotron child reduced the ten-turn worker CPU interval from 62.9 to 24.1 seconds, final-ASR mean from 1.604 to 1.379 seconds, and aggregate decode p95 from 426 to 310 ms.

The winning candidate passed a 100-turn accelerated soak, the 30-turn E2E gate, and one final 100-turn E2E run. Final E2E ASR was 1.019 s mean / 0.915 s p50 / 1.813 s p95 / 3.317 s max. TTFA was 1.831 / 1.594 / 3.327 / 4.680 s. First-to-last-25 ASR mean changed from 1.075 to 1.167 s (+8.5%), versus Phase 3's 1.542 to 2.620 s (+69.9%). First-to-last-25 TTFA improved from 2.135 to 1.827 s. Underrun and failure counts were zero.

### 3. Phase 3B blocker

The blocker was long-run latency growth inside `DecodeStream()`, with stable queue, memory, threads, handles, and owner scheduling. Phase 3 had early/late decode p50 near 378/609 ms and early/late final-ASR mean 1.542/2.620 s. Phase 3B isolates runtime execution from conversation and playback before changing production behavior.

### 4. Accelerated soak design

`scripts/diagnostics/nemotron-soak.py` speaks the production worker's little-endian loopback protocol directly. `realtime` mode sends PCM in real-time 20 ms chunks; `max-throughput` mode submits the same PCM immediately as a diagnostic stress test. Both retain CPU, two threads, greedy search, language auto, model paths, padding, `InputFinished`, and the remaining `IsReady` loop. Fixtures can rotate; same-fixture runs isolate runtime variance.

Each utterance stores accepted/processed/queued samples, first partial, finalize and wall time, every decode-call duration, per-stream p50/p95/max, scheduling gaps, throughput, working set, private bytes, CPU, threads, handles, and available Windows CPU counters. Temperature, package power, and explicit throttle sensors are marked unavailable rather than estimated.

### 5. Early-stop behavior

The first 5 or 10 utterances form the baseline and the latest 10 form the rolling window. The harness writes all data and reports `DEGRADATION DETECTED` when rolling decode p50, p95, or final-ASR mean exceeds 1.30x baseline. This is diagnostic early-stop only; the winning candidate still ran its configured 100 utterances.

### 6. Reproduction result

The first 40-turn worker soak was variable but did not cross its noisy five-turn baseline. A second run with a ten-turn baseline reproduced a sustained burst: utterances 20-23 had decode p50 464, 458, 549, and 437 ms and p95 476, 550, 594, and 498 ms. Rolling p95 crossed 1.30x baseline at utterance 23. There was no queue growth or resource leak.

### 7. Root cause

The root cause is sustained CPU package load from ORT thread-pool spinning, interacting with the Windows balanced-power policy and the i7-1260P hybrid processor's performance state. This is platform-sensitive, but the unnecessary spinning is a software-controllable contributor. Stream history and worker process lifetime alone were disproved: a separate 100-stream worker run returned to normal latency at stream 100.

### 8. CPU / thermal / power evidence

The test host is a 12th Gen Intel Core i7-1260P, 12 cores / 16 logical processors, on the Windows Balanced power plan. During sustained baseline inference, `% Processor Performance` commonly fell to 47-57%. After 5/15/30 seconds idle it recovered to 89/99/102%, while decode p50 improved to 270/252/254 ms. Across the idle experiment, decode p50 versus processor performance had correlation -0.685. The reported frequency counter stayed near 1.66 GHz and was not explanatory. Temperature, package power, and explicit throttle/PL sensors were unavailable.

### 9. ORT profiling result

ORT operator profiling was not enabled for the production benchmark. The blocker was reproduced and separated through per-call DecodeStream timing, process CPU, Windows performance-state counters, idle recovery, and a direct configuration A/B. Enabling profiling would have changed the timing under measurement without adding a necessary decision signal.

### 10. Early vs late profile

There is therefore no operator-level early/late ORT profile artifact. Aggregate DecodeStream timing and hardware counters show broad runtime slowdown rather than evidence for one retained stream or queue. This limitation is explicit.

### 11. `dynamic_block_base` A/B

`session.dynamic_block_base=4` was tested through sherpa-onnx's existing `SessionConfig.*` forwarding. Ten-turn final-ASR mean improved only from 1.604 to 1.559 s; aggregate decode p95 changed from 426 to 436 ms, worker CPU from 62.9 to 61.6 s. It was rejected because tail and CPU did not improve clearly.

### 12. Spinning A/B

| Candidate, 10 turns | final ASR mean | decode p50 | decode p95 | decode max | worker CPU |
|---|---:|---:|---:|---:|---:|
| ORT default | 1.604 s | 302 ms | 426 ms | 768 ms | 62.9 s |
| dynamic block 4 | 1.559 s | 290 ms | 436 ms | 633 ms | 61.6 s |
| spinning disabled | 1.379 s | 268 ms | 310 ms | 446 ms | 24.1 s |
| dynamic block 4 + no spinning | 1.360 s | 253 ms | 334 ms | 683 ms | 23.5 s |

Spinning disabled alone won on correctness, p95/max, CPU, and simplicity. The combined candidate's slightly better mean did not compensate for its worse tail.

### 13. Thread-pool analysis

sherpa-onnx sets both ORT intra-op and inter-op counts to `NEMOTRON_THREADS`, which remains 2. Phase 3B changes only `session.intra_op.allow_spinning` and `session.inter_op.allow_spinning` to `0` inside the Nemotron child. It does not change thread count, introduce a global pool, or alter session ownership.

### 14. P/E-core / scheduling analysis

Per-logical-processor performance, utility, and frequency were captured. The workload migrates across the 16 logical processors and the host has a hybrid topology, but no evidence justified affinity. AboveNormal remained rejected and no priority or P-core binding was added. Windows remained free to schedule the two inference threads.

### 15. sherpa internal analysis

Accepted/processed samples, bounded queue, stream destruction, and remaining finalize decode remained correct. Worker-only realtime 30-turn and max-throughput 100-turn baselines showed no monotonic stream-history effect. The late latency is in DecodeStream/ORT execution rather than feature input, result extraction, finalize queueing, or stream retention.

### 16. Idle recovery result

In the same worker process, 5/15/30-second pauses improved decode p50 from the preceding sustained range to 270/252/254 ms and final time to 1.266/1.150/1.142 s. Subsequent immediate turns rose gradually again. This supports power/performance-state recovery rather than a retained-stream leak.

### 17. Worker restart result

Fresh workers repeatedly returned to their initial range. Restart was used only as diagnosis; periodic production restart was rejected because it would hide the internal load pattern, add model-load latency, and violate persistent-worker intent.

### 18. Adopted correction

The Engine starts only its dedicated Nemotron worker with `SHERPA_ONNX_ORT_DISABLE_SPINNING=1`. sherpa-onnx session creation recognizes that opt-in and adds the two official ORT session entries. The default for every other sherpa executable remains unchanged. The worker also supports diagnostic per-decode detail and optional performance-log redirection; both are inactive unless explicitly selected.

### 19. Rejected experiments

Dynamic block base alone and in combination were rejected on p95/max and complexity. AboveNormal, affinity, P-core binding, worker restart, larger queues, timeout changes, GPU, another runtime, and another model were not adopted. ORT profiling was unnecessary for the decision. No speculation, playback, TTS, Smart Turn, or Whisper change was made in Phase 3B.

### 20. 10-turn result

The no-spinning A/B run completed 10/10: final-ASR mean 1.379 s, aggregate decode p50/p95/max 268/310/446 ms, CPU 24.1 s. The one-turn production E2E smoke also completed normally before the gate run.

### 21. Accelerated soak final result

The winning no-spinning candidate completed 100/100 max-throughput utterances without early-stop. Most decode p50 values remained 253-303 ms. A transient rise around utterances 83-85 recovered without restart; utterance 100 was 277 ms. There were no errors, queue overflows, or resource failures.

### 22. Realtime soak final result

The baseline worker-only realtime 30-turn run completed without degradation: after warm-up, decode p50 was generally 234-280 ms, with one isolated p95 spike. It demonstrated that real-time chunk cadence and stream history were not the source. The adopted correction was then validated through the full E2E runs rather than repeating a lower-value isolated realtime soak.

### 23. 30-turn E2E

| Group | Final ASR mean / p50 / p95 / max | TTFA mean / p50 / p95 / max |
|---|---|---|
| all 30 | 0.947 / 0.834 / 1.273 / 1.992 s | 1.725 / 1.457 / 2.940 / 3.277 s |
| first 10 | mean 1.051 s | mean 2.120 s |
| middle 10 | mean 0.966 s | mean 1.511 s |
| last 10 | mean 0.823 s | mean 1.544 s |

Underrun, hard failure, worker/provider error, cancellation, queue overflow, PCM drop, and playback completion failure were zero. The gate passed, permitting exactly one final 100-turn E2E run.

### 24. Final 100-turn E2E

The four-fixture rotation completed 100/100 in one persistent process. Final ASR was 1.019/0.915/1.813/3.317 s mean/p50/p95/max. First render was 1.831/1.594/3.327/4.680 s. Speculation promoted 74 and mismatched 26 turns. All generations completed.

### 25. First / middle / last 25 comparison

| Group | Final ASR mean | TTFA mean | stream decode-p50 mean | stream decode-p95 mean |
|---|---:|---:|---:|---:|
| first 25 | 1.075 s | 2.135 s | 226 ms | 277 ms |
| 26-50 | 0.854 s | 1.596 s | 180 ms | 217 ms |
| 51-75 | 0.980 s | 1.765 s | 220 ms | 295 ms |
| last 25 | 1.167 s | 1.827 s | 294 ms | 447 ms |

Decode tail variance remains visible, but it no longer produces the Phase 3 user-level degradation: first-to-last Final ASR grew 8.5%, while TTFA improved 14.4%. The final values remain well below Phase 3's last-25 ASR mean of 2.620 s.

### 26. Final ASR p50 / p95 / max

Mean **1.019 s**, p50 **0.915 s**, p95 **1.813 s**, max **3.317 s**.

### 27. TTFA p50 / p95 / max

Mean **1.831 s**, p50 **1.594 s**, p95 **3.327 s**, max **4.680 s**.

### 28. DecodeStream p50 / p95 / max

Across the 100 per-stream aggregates: p50-of-p50 **183 ms**, p95-of-p50 **412 ms**; p50-of-p95 **239 ms**, p95-of-p95 **788 ms**; p50 stream max **289 ms**, p95 stream max **909 ms**, absolute max **1.336 s**. These tails are retained in the report rather than hidden, although E2E stability meets the practical gate.

### 29. Underrun / failure counts

AudioWorklet underrun 0/100; AquesTalk hard failure 0; worker crash 0; provider error 0; silent PCM drop 0; queue overflow 0; stale generation contamination observed 0; cancellation 0; playback completion failure 0.

### 30. CPU / resource trend

Worker CPU increased from 3.9 to 608.2 seconds over the approximately 38-minute final run. The Phase 3 run had reached 2,794 seconds, so spinning removal materially reduced sustained CPU even though the final wall-clock run was longer. System `% Processor Performance` was about 70% at sampled start/middle/end; no progressive collapse like the reproduced baseline burst was observed in Final ASR.

### 31. Memory / thread / handle / goroutine trend

The worker warmed from 759 to 796 MiB working set, then stayed exactly 796 MiB at midpoint/end; private bytes stayed 819 MiB, threads 5, handles 151. Engine working set was 22.0/37.2/37.8 MiB, threads 9/17/18, handles 140/379/297. Handles fell after the midpoint. Existing performance snapshots kept Engine goroutines near 12-13. There is no linear leak evidence.

### 32. JA / EN result

Short, medium, and long Japanese plus English fixtures rotated through all 100 turns with `language=auto`. JA竊脱N and EN竊谷A turn boundaries completed without provider error. Audible and semantic real-microphone quality remains part of manual acceptance.

### 33. Whisper preservation

No Whisper source, worker protocol, model, or configuration changed. Go/shared transport tests pass. A runnable local Whisper runtime binary is still absent, so Whisper E2E remains explicitly unexecuted; nothing was downloaded or replaced.

### 34. Public API / SDK compatibility

REST v1, WebSocket event schemas, TypeScript SDK, Python SDK, Browser Voice public behavior, credit-v1, Smart Turn, speculation exact-match safety, LLM provider/model, and AquesTalk settings are unchanged.

### 35. Automated test result

- `go test ./...`: pass.
- `go build ./cmd/engine`: pass.
- TypeScript SDK and AudioWorklet: 21/21 pass.
- Python SDK: 13/13 pass.
- AquesTalk regression, including sequential and serialized-concurrency coverage: pass.
- Nemotron worker Release build: pass.
- diagnostic smoke, realtime soak, baseline accelerated reproduction, A/B, winning 100-turn accelerated soak, 30-turn E2E, final 100-turn E2E: pass as documented.
- Launcher Ctrl+C cleanup: Engine, Smart Turn, Browser, and worker stopped; ports 8765, 8766, and 8080 free.
- `git diff --check`: pass in both repositories.

### 36. Manual microphone acceptance procedure

Run `./scripts/dev.ps1 nemotron -Performance`, open Browser Voice, and perform these seven checks:

1. Normal Japanese: one final transcript, one response, audible PCM, no underrun/error.
2. Long Japanese: complete transcript with no truncation/drop and bounded finalization.
3. Barge-in during AI playback: old generation cancels, old PCM stops, new speech/transcript/response completes, no mixed audio.
4. False endpoint/resume: say `莉頑律縺ｯ窶ｦ窶ｦ繧・▲縺ｱ繧頑・譌･縺ｮ隧ｱ繧偵＠繧医≧`; Smart Turn continues, no premature commit, resumed audio stays in the same logical utterance.
5. Self-correction: say `譛ｭ蟷後・螟ｩ豌冷ｦ窶ｦ縺・ｄ縲∵溜蟾昴・隧ｱ`; only the corrected final meaning is committed and speculative mismatch is handled safely.
6. Japanese then English: both recognized with `language=auto`, with no restart or fixed-language setting.
7. English then Japanese: same conditions in reverse.

After each case confirm the Browser console and Engine log contain no worker/provider error, stale generation, queue overflow, PCM drop, or underrun. End with one Ctrl+C and verify ports 8765/8766/8080 are free. Passing all seven permits the final `PERFORMANCE / STABILITY WORK COMPLETE` verdict.

### 37. Remaining limitations

Physical-microphone and audible correctness are pending. Decode p95/max still show platform-dependent bursts even though user-visible late-run ASR/TTFA degradation is practically resolved. Temperature/package-power sensors and an operator-level ORT profile were unavailable/not needed, so the hardware mechanism is identified through Windows effective-performance counters and controlled A/B rather than direct watt/temperature telemetry. Whisper E2E is blocked by the absent local binary.

### 38. Final architecture

Whisper retains its existing path. Nemotron retains persistent CPU2 streaming with bounded ingress, one owner, continuous decode, partial/final results, Smart Turn/speculation, common LLM/AquesTalk, credit-v1, and the 300 ms bounded Worklet startup watermark. The only runtime correction in Phase 3B is parking ORT intra/inter worker threads instead of spinning between Nemotron decode work.

### 39. Changed files

- `internal/providers/stt/nemotron/provider.go`: opt the Nemotron child into no-spinning ORT sessions.
- `sherpa-onnx/csrc/session.cc`: recognize the child-only opt-in; retain default behavior otherwise.
- `sherpa-onnx/csrc/koehaku-streaming-asr-worker.cc`: diagnostic per-call decode and optional raw performance log.
- `scripts/diagnostics/nemotron-soak.py`: accelerated/realtime soak, early-stop, process and Windows counter capture.
- `scripts/diagnostics/run-perf-browser.mjs`: repeatable headless real-WebSocket/AudioWorklet E2E runner.
- `scripts/diagnostics/sample-phase3b-resources.ps1`: final-run process/resource sampling.
- `scripts/diagnostics/ort-*.config`: diagnostic-only A/B inputs.
- `docs/benchmarks/phase3b-*`: raw and summarized evidence.
- this report.

### 40. Git diff summary

The production correction is one Nemotron-child environment opt-in plus two ORT session config entries when that opt-in is present. All other Phase 3B additions are diagnostics, tests/runners, raw artifacts, and documentation. No dependency version, model, decoder, thread count, provider, Whisper implementation, API, SDK contract, Smart Turn/speculation semantic, TTS behavior, or playback architecture changed. The official `sherpa-onnx-microphone.cc` remains unchanged.

Phase 3B primary artifacts:

- `../../benchmark-results/2026-10-04/phase3b-max-throughput-long.json`
- `../../benchmark-results/2026-10-04/phase3b-idle-recovery.json`
- `../../benchmark-results/2026-10-04/phase3b-ab-baseline-10.json`
- `../../benchmark-results/2026-10-04/phase3b-ab-dynamic-block-10.json`
- `../../benchmark-results/2026-10-04/phase3b-ab-no-spinning-10.json`
- `../../benchmark-results/2026-10-04/phase3b-ab-combined-10.json`
- `../../benchmark-results/2026-10-04/phase3b-no-spinning-accelerated-100.json`
- `../../benchmark-results/2026-10-04/phase3b-e2e-30.json`
- `../../benchmark-results/2026-10-04/phase3b-final-e2e-100.json`
- `../../benchmark-results/2026-10-04/phase3b-final-worker.log`
- `../../benchmark-results/2026-10-04/phase3b-final-resources.csv`
- `../../benchmark-results/2026-10-04/phase3b-final-summary.json`




## Public-release Whisper blocker correction

The first Whisper release gate stopped at 19/30. Instrumentation separated queue wait, WAV preparation, HTTP inference, parsing, restart time and cancellation. WAV preparation and parsing were normally milliseconds; CPU inference dominated. The critical fault was lifecycle coupling: the 45-second speculation lifetime began before snapshot STT, and its expiry could cancel STT after Smart Turn had already committed that work. HTTP cancellation terminated the persistent server, so fallback reloaded the model and repeated inference.

The correction keeps timeout cancellation for uncommitted speculation, preserves committed canonical STT, and starts a fresh lifetime after STT for speculative LLM work. The same Whisper model, language `auto`, 4 threads, beam size 5 and best-of 5 remain. Post-fix runs completed 30/30 and 100/100 with zero AudioWorklet underruns and no speculation-timeout cancellation cascade. The 100-turn Final ASR mean/p50/p95/max was 35.456/34.947/47.159/78.007 seconds; first render was 36.686/36.544/48.473/80.968 seconds. CPU inference remains slow, but the correctness and long-run progression blocker is removed.
