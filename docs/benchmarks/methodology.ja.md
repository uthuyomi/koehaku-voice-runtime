# STT・リアルタイムベンチマーク測定方法

[English](methodology.md)

精度と STT 確定遅延は、provider 共通の `scripts/benchmarks/stt-accuracy.py` から公開 `WS /v1/transcription` を通して測定します。全経路測定は Browser Voice 診断 harness を使い、Smart Turn、投機実行、LLM、TTS、WebSocket、AudioWorklet render を含みます。

`corpus-manifest.json` は日本語 5 件、英語 5 件の会話文を持ち、短文・中文・長文・数字・固有名詞を含みます。`generate-corpus.ps1` がインストール済み Windows 音声で 16 kHz mono PCM16 WAV をローカル生成します。音声ファイルは Git の対象外で、配布しません。端末上では再現できますが、マイク雑音、訛り、室内音響を表すものではありません。

両 provider へ同じ WAV、packet size、実時間 pacing、終了信号、正規化を適用します。日本語は Unicode 正規化後の CER、英語は Unicode・大文字小文字正規化後の WER を使います。最初の repetition を精度値とし、現在の遅延測定は各発話 2 repetition です。provider 固有設定は隠さず記録し、decoder や model を不自然に変更しません。

STT finalization latency は input 終了送信から final transcript 受信まで、E2E Final ASR は `speech_end` から transcript commit まで、first render は `speech_end` から応答の最初の AudioWorklet render までです。可聴 TTFA は実スピーカー・実マイク測定が必要なため、自動試験では SKIP です。

同じ端末、OS、corpus、公開 protocol、packetization、harness を使用します。合成音声の精度試験と過去の Nemotron 全経路試験は目的が異なるため、一つの順位へ混ぜません。実マイク、可聴品質、自然さは manual acceptance として残します。
