# AI / Coding Agent 保守ガイド（日本語補助資料）

Coding agent 向け運用規則の正本はリポジトリ直下の [AGENTS.md](../../AGENTS.md) です。この文書は日本語開発者向けの補助資料であり、規則に差がある場合は英語正本を優先します。

本プロジェクトは、PCM/VAD、STT、Smart Turn、投機実行、ストリーミング LLM、発話チャンク化、TTS、再生フィードバックを管理するリアルタイム音声会話ランタイムです。Whisper の既存経路と Nemotron 専用 Fast Path は分離されています。

変更時は次を守ってください。

- Nemotron の変更へ Whisper を巻き込まない。
- Nemotron failure を Whisper へ暗黙 fallback しない。
- Smart Turn の確定権限を維持し、partial transcript を確定事実として扱わない。
- 音声キューは上限付きとし、受理済み PCM を黙って捨てない。
- session、stream、generation、epoch の所有権と stale event 防止を維持する。
- 公開 API v1、SDK wire event、`credit-v1` の累積 source-frame accounting を壊さない。
- 性能変更は同一条件で変更前後を測り、raw data を保存する。
- 実行できない試験を PASS とせず、理由付きの SKIP とする。

最低限の手順は、関連コードとテストを読む、最小の変更を行う、対象テストを追加する、`go test ./...` と `go build ./cmd/engine` を実行する、SDK・Browser・provider 境界の試験を行う、最後に diff・文書リンク・秘密情報・ローカルパス・生成物を監査する、という順です。

Phase 3B の性能調査、採用した ORT no-spinning、採用しなかった案は [技術調査報告](../benchmarks/realtime-performance-investigation-2026-10-04.md) を参照してください。実マイク、実スピーカー、可聴品質は自動試験だけでは完了判定できません。
