# Koehaku Voice Runtime public release preparation — 2026-10-04

## Verdict

**READY FOR USER REVIEW BEFORE PUSH**

All automated publication blockers completed. Physical microphone and audible-output acceptance remains a separate owner review item. Nothing was committed, pushed, tagged, released, or published.

## Environment and rename

Validation ran on Windows 11 build 26300, Intel Core i7-1260P, 31.7 GiB RAM. Public branding, Go module identity, SDK distributions, examples, launcher output, schemas, and current documentation use **Koehaku Voice Runtime**. The canonical description is “Open-source local-first realtime voice runtime for AI.” Old SDK class/import/CLI and environment identifiers remain documented compatibility aliases. Historical benchmark records retain their original identity where rewriting would falsify evidence.

## Whisper blocker

The previous run stopped at 19/30. A 45-second speculation timer covered snapshot STT even after Smart Turn committed it as canonical work. Slow but valid CPU inference was cancelled, cancellation killed the persistent whisper server, and the fallback path reloaded the model and retranscribed the audio. The timer now cancels only uncommitted speculative STT; committed STT may finish, and a fresh timer starts for the subsequent speculative LLM request. Focused tests assert a committed request can exceed the old lifetime with one STT call.

Whisper `small`, CPU, 4 threads, best-of 5, beam size 5, language `auto`:

- Accuracy 20/20: Japanese CER 11.96%, English WER 10.00%; request/final mean/p50/p95/max 22.275/20.734/30.085/31.550 s.
- E2E 30/30: Final ASR 27.668/20.886/44.606/70.660 s; first render 28.648/21.687/45.627/72.491 s; underruns/errors 0.
- E2E 100/100: Final ASR 35.456/34.947/47.159/78.007 s; first render 36.686/36.544/48.473/80.968 s; underruns/external turn failures 0.
- First/middle/last Final ASR means: 32.487/37.659/34.017 s. No turn-number growth.
- One Smart Turn continue/resume cancellation invalidated an in-flight speculative snapshot and caused one server restart; the canonical turn recovered. Speculation-timeout cancellations and cascading stalls were 0.

## Nemotron

The Phase 3B production baseline is preserved: 100/100; Final ASR mean/p50/p95/max 1.019/0.915/1.813/3.317 s; first render 1.831/1.594/3.327/4.680 s; all documented failure counters and underruns 0. `SHERPA_ONNX_ORT_DISABLE_SPINNING=1` remains scoped to the Nemotron child.

The final shared-layer regression completed 30/30 with underruns/errors 0. Final ASR was 1.617/1.116/3.846/5.433 s and first render 2.519/1.860/5.997/6.613 s. This host run was slower than Phase 3B, so it is reported without replacing or claiming an improvement over the preserved baseline. The Whisper fix is confined to snapshot speculation and does not enter Nemotron's streaming single-owner path.

## Stability and resources

Whisper-server working set changed 765.4→771.7 MiB and private bytes 1255.7→1263.2 MiB over 174 samples. Engine working set changed 64.2→40.2 MiB and private bytes 57.8→64.6 MiB. No linear resource growth was observed. Launcher shutdown released ports 8765, 8766, and 8080 after both providers.

## Repository and documentation

English and Japanese overview, quickstart, configuration, provider and benchmark material are linked from the landing pages. `AGENTS.md` is the canonical maintainer guide. Public API, protocol and SDK documents cover capability discovery and consumer integration. Experimental binaries, models, generated WAVs, build trees, virtual environments, `.env`, and local scratch remain excluded. Reproducible sherpa-onnx worker source, patch and CMake instructions live under `third_party/sherpa-onnx/`.

The final working-tree classification is recorded in the [commit candidate manifest](release/commit-candidate.md). Every remaining nonignored modified, deleted, and untracked path belongs to an intended source, test, documentation, benchmark, SDK, license, or integration group. Two uncited benchmark runs made with an unintended fixed-Japanese configuration were removed as invalid duplicate release evidence; the original Whisper 19/30 failure remains preserved.

The final unstaged candidate has 105 modified entries, 2 intentional deletions, and 76 intended untracked files represented by 15 top-level status entries. `git add -A` is safe after owner review of the manifest and diff. Git attributes already normalize ordinary text automatically; CRLF-to-LF notices are staging-policy notices, while the diff itself contains no repository-wide line-ending rewrite.

## Security and licensing

The candidate-file release checker found no known credential signature, broken local documentation link, or prohibited machine path. Reachable-history review is filename/signature based and cannot prove absence. No runtime binary, model, AquesTalk component, or generated corpus is selected for publication. `THIRD_PARTY_NOTICES.md` distinguishes source dependencies, local runtime dependencies, user-supplied models/binaries, and external services. The owner must still confirm AQUEST redistribution/commercial rights, exact binary transitive notices, and downloaded model revision terms before distributing binaries or models.

## Automated test matrix

PASS:

- `go test ./...` and `go build ./cmd/engine`
- TypeScript SDK 21/21 and package smoke
- Python SDK 13/13 and wheel/package smoke
- Smart Turn sidecar 3/3
- release checker unit tests 6/6 and candidate scan
- `git diff --check`
- Nemotron worker Release build, startup, Go protocol/provider tests, final 30-turn regression
- Whisper startup, auto-language accuracy, 30-turn and 100-turn E2E
- Browser Voice/AudioWorklet tests through the TypeScript suite
- launcher startup/shutdown; ports 8765/8766/8080 free

SKIP/manual:

- New physical microphone and speaker listening
- Audible TTFA/naturalness, audible barge-in, old-speech resurrection, false endpoint/pause-resume and live JA/EN switching
- Publishing packages, tag, GitHub release, remote changes, commit or push

## Remaining limitations and user actions

Whisper CPU latency is high despite the lifecycle correction. Accuracy uses five synthetic references per language. First render is not audible speaker onset. Before push, the owner should review the full diff and selected raw artifacts, perform manual physical-audio acceptance, choose a release version, rename/create the GitHub repository, update the remote manually, and confirm redistribution terms. `v0.2.0` remains a reasonable recommendation because the provider-neutral rename and realtime Fast Path are substantial while Public API v1 remains compatible; no version was finalized.
