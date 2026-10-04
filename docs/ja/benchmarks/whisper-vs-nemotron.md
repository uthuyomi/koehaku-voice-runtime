# Whisper と Nemotron の比較ベンチマーク

[English](../../en/benchmarks/whisper-vs-nemotron.md) | [計測方法](../../benchmarks/methodology.ja.md) | [生データ](../../../benchmark-results/2026-10-04/)

## 実行条件

Windows 11、Intel Core i7-1260P、31.7 GiB RAMで、同じ16 kHz mono PCM fixtureをPublic API v1へ入力した。Nemotronは560 ms INT8、CPU、2 threads、greedy search、language=`auto`、persistent worker、ORT no-spinning。Whisperはwhisper.cpp `small`、CPU、4 threads、best-of 5、beam size 5、language=`auto`、persistent serverを使用した。

## 認識精度

| 指標 | Whisper | Nemotron |
|---|---:|---:|
| 日本語CER | 11.96% | 2.17% |
| 英語WER | 10.00% | 7.50% |
| Request/final mean | 22.275 s | 4.287 s |
| Request/final p50 | 20.734 s | 3.820 s |
| Request/final p95 | 30.085 s | 6.670 s |
| Request/final max | 31.550 s | 7.321 s |

各言語5件のローカル合成音声による回帰比較であり、実環境全般の精度を示す値ではない。

## Whisper blockerの原因と修正

旧Whisper E2Eは19/30で停止した。原因はsnapshot STT開始前から45秒のspeculation timerを動かしていたことだった。CPU推論が45秒を越えると、Smart Turnがcommit済みでもcanonical HTTP inferenceをcancelした。cancel時にpersistent serverを終了するため、normal pathがモデルを再ロードし、同じ音声を再推論した。これが45秒cancel、モデル再ロード、重複推論の連鎖を作った。

snapshot STT中は未commitの投機処理だけをtimer対象にした。Smart Turn commit後はtimer expiryでcanonical STTをcancelしない。STT完了後、speculative LLM用の新しいlifetimeを開始する。Public API、Smart Turnの意味、Whisper decoder設定、Nemotron single-owner streaming pathは変更していない。timeoutを越えるcommit済みSTTが1回だけ実行されることをtestで固定した。

## Realtime E2E

| 指標 | Whisper 30-turn | Whisper 100-turn | Nemotron Phase 3B 30-turn | Nemotron Phase 3B 100-turn | Nemotron最終回帰30-turn |
|---|---:|---:|---:|---:|---:|
| 完了turn | 30/30 | 100/100 | 30/30 | 100/100 | 30/30 |
| Final ASR mean | 27.668 s | 35.456 s | 0.947 s | 1.019 s | 1.617 s |
| Final ASR p50 | 20.886 s | 34.947 s | 0.834 s | 0.915 s | 1.116 s |
| Final ASR p95 | 44.606 s | 47.159 s | 1.273 s | 1.813 s | 3.846 s |
| Final ASR max | 70.660 s | 78.007 s | 1.992 s | 3.317 s | 5.433 s |
| First render mean | 28.648 s | 36.686 s | 1.725 s | 1.831 s | 2.519 s |
| First render p50 | 21.687 s | 36.544 s | 1.457 s | 1.594 s | 1.860 s |
| First render p95 | 45.627 s | 48.473 s | 2.940 s | 3.327 s | 5.997 s |
| First render max | 72.491 s | 80.968 s | 3.277 s | 4.680 s | 6.613 s |
| AudioWorklet underrun | 0 | 0 | 0 | 0 | 0 |
| 外部turn failure | 0 | 0 | 0 | 0 | 0 |

Whisper 100-turnのfirst/middle/last群のFinal ASR meanは32.487/37.659/34.017秒、first render meanは33.645/38.946/35.206秒で、turn番号に比例する悪化はなかった。speculationは99 promotion、1 mismatch。Smart Turn continue/resumeによる正当な投機snapshot cancelが1回ありserverを再初期化したが、canonical turnは回収された。speculation timeout由来のcancelと停止turnは0だった。

Nemotron最終回帰は機能面で完走したが、保存済みPhase 3Bより遅かった。production変更はWhisperが使用するsnapshot speculationに閉じ、Nemotron single-owner streaming pathへ入らないため、この結果をPhase 3B baselineと差し替えず、host jitterを含む再測定として併記する。

## Resource

Whisper 100-turnの174 sampleで、whisper-server working setは765.4→771.7 MiB、private bytesは1255.7→1263.2 MiBだった。最大65 threads、194 handles。Engine working setは64.2→40.2 MiB、private bytesは57.8→64.6 MiB。線形増加は観測しなかった。

## 制約

first renderはBrowser timing markerで、実スピーカーの可聴開始ではない。実マイク、部屋雑音、accent、barge-inの体感、音声自然性は手動確認が必要。Whisperのcorrectness blockerは解消したが、CPU latencyは高い。

## 生データ

- [Whisper auto accuracy](../../../benchmark-results/2026-10-04/public-whisper-accuracy-koehaku.json)
- [旧Whisper未完了run](../../../benchmark-results/2026-10-04/public-whisper-e2e-partial.json)
- [修正後Whisper 30-turn](../../../benchmark-results/2026-10-04/public-whisper-auto-e2e-30.json)
- [修正後Whisper 100-turn](../../../benchmark-results/2026-10-04/public-whisper-auto-e2e-100.json)
- [Whisper resource](../../../benchmark-results/2026-10-04/public-whisper-auto-e2e-100-resources.csv)
- [Whisper最終診断summary](../../../benchmark-results/2026-10-04/public-whisper-final-summary.json)
- [Nemotron accuracy](../../../benchmark-results/2026-10-04/public-nemotron-accuracy.json)
- [Nemotron Phase 3B 30-turn](../../../benchmark-results/2026-10-04/phase3b-e2e-30.json)
- [Nemotron Phase 3B 100-turn](../../../benchmark-results/2026-10-04/phase3b-final-e2e-100.json)
- [Nemotron最終回帰](../../../benchmark-results/2026-10-04/public-nemotron-final-e2e-30.json)
