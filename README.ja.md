# Koehaku Voice Runtime

AIのための、オープンソースでローカルファーストなリアルタイム音声ランタイムです。

Koehaku（コエハク）は「声」と「拍」を組み合わせた名前で、リアルタイム会話に必要な発話タイミング、応答、割り込み、会話の流れを表します。

[English](README.md) | 日本語

Koehaku Voice Runtime は、セルフホスト型のリアルタイム音声会話ランタイムです。マイク PCM、STT、ターン判定、投機的・ストリーミング LLM、発話チャンク化、TTS、再生フィードバックを一つのセッションライフサイクルとして扱います。

現在は既存の whisper.cpp 経路と、Nemotron 3.5 Streaming ASR 専用 Fast Path を提供します。STT、LLM、ターン判定、TTS はそれぞれ provider 境界を維持しています。サンプル構成は外部の OpenAI 互換 LLM サービスと、別途入手する AquesTalk 資産を使います。プロプライエタリ資産、モデル、認証情報は同梱しません。

## 実行フロー

```text
マイク -> VAD / PCM -> STT -> Smart Turn -> 会話の確定
                                |                |
                                +-> 投機実行 ----+-> ストリーミング LLM
                                                   -> 発話チャンク
                                                   -> TTS -> PCM
                                                   -> AudioWorklet
```

リアルタイム経路は barge-in、generation 単位のキャンセル、古いイベントの隔離、上限付き音声キュー、`credit-v1` 再生フロー制御、実際の再生を反映した会話履歴に対応します。Smart Turn が確定判断の権限を持ち、投機結果は promotion 前に公開されません。

## クイックスタート

Engine の対象は Windows x64 です。ビルドには [`go.mod`](go.mod) 記載の Go、Browser Voice 開発には Node.js、Smart Turn と Python SDK には Python が必要です。runtime、モデル、AquesTalk/AqKanji2Koe 資産、API 認証情報はローカルで用意してください。

```powershell
git clone https://github.com/uthuyomi/koehaku-voice-runtime.git
cd koehaku-voice-runtime
Copy-Item .env.example .env
go build -o dist/engine.exe ./cmd/engine
```

`.env` で provider を設定し、開発 launcher を実行します。

```powershell
./scripts/dev.ps1 whisper
./scripts/dev.ps1 nemotron
```

launcher は Engine、Smart Turn sidecar、Browser Voice server を起動します。Nemotron worker を二重起動せず、persistent worker の管理は Engine に任せます。Browser Voice は `http://127.0.0.1:8080/examples/typescript/browser-voice/` です。Ctrl+C を一度押すと launcher 所有の process tree を終了します。

[クイックスタート](docs/quickstart.ja.md)、[設定](docs/configuration.ja.md)、[Provider](docs/providers.ja.md)、[トラブルシューティング](docs/troubleshooting.ja.md)も参照してください。

## 公開インターフェース

- `GET /health`
- `GET /v1/capabilities`
- `POST /v1/audio/speech`
- `WS /v1/realtime`
- `WS /v1/transcription`
- [TypeScript SDK](sdk/typescript/README.md)
- [Python SDK](sdk/python/README.md)

互換性は [API](docs/api.md)、[Realtime protocol](docs/realtime-protocol.ja.md)、[Protocol versioning](docs/protocol-versioning.md)に記載しています。

## ベンチマーク

公開ベンチマークでは両 STT provider に同じ生成済み二言語コーパスと公開 transcription WebSocket を使用します。精度、確定遅延、全経路 TTFA、安定性、リソースを区別して報告します。生成音声は再現可能な試験入力であり、実マイク受入試験の代替ではありません。

- [Whisper と Nemotron の比較](docs/ja/benchmarks/whisper-vs-nemotron.md)
- [測定方法](docs/benchmarks/methodology.ja.md)
- [Phase 3/3B 技術調査](docs/benchmarks/realtime-performance-investigation-2026-10-04.md)
- [機械可読な結果](benchmark-results/2026-10-04/)

確立済みの Nemotron Phase 3B 100-turn 試験は 100/100 turn を完了し、worker/provider error、queue overflow、音声 drop、再生 failure、AquesTalk error、AudioWorklet underrun はすべて 0 でした。Final ASR は mean 1.019 s / p50 0.915 s / p95 1.813 s / max 3.317 s、first render は mean 1.831 s / p50 1.594 s / p95 3.327 s / max 4.680 s です。この値は文書記載の端末、モデル、fixture、定義に限定され、first render は実マイクから実スピーカーまでの可聴 TTFA ではありません。

## 開発

```powershell
go test ./...
go build ./cmd/engine
npm --prefix sdk/typescript test
py -3.12 -m unittest discover -s sdk/python/tests
```

ランタイム変更前に [CONTRIBUTING.md](CONTRIBUTING.md) を確認してください。Coding agent 用の正本は英語の [AGENTS.md](AGENTS.md)、日本語の補助資料は [AI 保守ガイド](docs/ja/ai-maintenance-guide.md) です。

## ライセンスとセキュリティ

プロジェクト独自のコードと文書は [MIT License](LICENSE) の対象です。第三者 runtime、モデル、プロプライエタリ音声資産には個別の条件があります。[Third-party notices](THIRD_PARTY_NOTICES.md) を確認してください。`.env`、認証情報、モデル、runtime binary、AQUEST 資産を commit しないでください。脆弱性の報告方法は [SECURITY.md](SECURITY.md) に記載しています。

現在はローカルで公開前レビュー中です。version、tag、release、公開操作は所有者が決定します。
