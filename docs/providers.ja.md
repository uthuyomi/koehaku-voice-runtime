# Provider 設定

Koehaku Voice Runtime は STT、ターン判定、LLM、TTS を独立した provider 境界として扱います。モデル、実行ファイル、API 認証情報、AquesTalk 資産は同梱しません。共通設定は [設定リファレンス](configuration.ja.md)、起動手順は [クイックスタート](quickstart.ja.md) を参照してください。

## AquesTalk / AqKanji2Koe

Windows adapter は利用者が用意した AquesTalk1 DLL と AqKanji2Koe DLL・辞書を動的に読み込み、mono 8 kHz WAV を返します。Koehaku はこれらの proprietary asset やライセンスキーを配布しません。利用・再配布条件は AQUEST の製品ページと契約を確認してください。

標準の voice は `f1`、標準速度は倍率 `1.0` です。空白のみ、変換後に音声記号が空になる入力、未知の voice、native 合成失敗は安全な generation error になります。通常ログへ会話本文は出しません。明示的なローカル診断に限り `KOEHAKU_TTS_DIAGNOSTIC_DIR` を使用できます。旧 `YUKKURI_TTS_DIAGNOSTIC_DIR` は互換 alias です。

## whisper.cpp STT

Whisper は既存の final-only 経路を使います。既定 runtime は persistent server で、1つの model/context を共有し推論を直列化します。`process` mode は診断用です。model、language、thread、beam size、best-of は環境設定をそのまま使用し、別 provider への silent fallback は行いません。

CPU 上の Whisper 推論は発話によって speculation の既定 lifetime より長くなる場合があります。commit 済み snapshot の STT は正解生成に必要な処理なので、LLM speculation の lifetime とは分離されています。キャンセル、queue 待ち、model reload、推論時間は runtime telemetry で区別します。

## Nemotron Streaming ASR

`STT_PROVIDER=nemotron` は専用 Realtime Streaming Fast Path を有効にします。Engine が localhost の persistent worker を1つ起動し、turn ごとに `OnlineStream` を作成します。Browser からの入力は PCM16 mono 16 kHz です。partial は内部 hint であり、Smart Turn が commit を決めるまで会話履歴へ確定しません。

baseline は Nemotron 3.5 Streaming ASR 0.6B 560 ms INT8、CPU、2 threads、`greedy_search`、language auto です。worker/model/queue の失敗時に Whisper へ silent fallback しません。

```dotenv
STT_PROVIDER=nemotron
NEMOTRON_WORKER_EXECUTABLE=C:\path\to\sherpa-onnx-nemotron-worker.exe
NEMOTRON_ENCODER=C:\path\to\encoder.onnx
NEMOTRON_DECODER=C:\path\to\decoder.onnx
NEMOTRON_JOINER=C:\path\to\joiner.onnx
NEMOTRON_TOKENS=C:\path\to\tokens.txt
NEMOTRON_THREADS=2
NEMOTRON_LANGUAGE=auto
NEMOTRON_PERFORMANCE=true
```

現在の worker は sherpa-onnx v1.13.8 に project 固有の target と opt-in の ORT no-spinning patch を適用して build します。公開可能な source と手順は [`third_party/sherpa-onnx`](../third_party/sherpa-onnx/README.md) にあります。`SHERPA_ONNX_ORT_DISABLE_SPINNING=1` は Engine が Nemotron child にだけ設定し、他の process や upstream の既定動作には適用しません。

## Smart Turn

Smart Turn は STT ではなく endpoint 判定です。既定では loopback の `http://127.0.0.1:8766/predict` を使い、直近最大8秒の PCM16 mono 16 kHz から発話完了確率を返します。Smart Turn を bypass せず、失敗時の既存 endpoint policy と error handling を維持します。model と Python 環境はローカル依存です。

## LLM

LLM provider は OpenAI-compatible streaming API を利用できます。認証情報は `.env` などのローカル設定で渡し、source、log、benchmark artifact へ保存しません。speculation は private buffer に出力を保持し、final transcript と安全に一致した場合だけ promotion します。

## ライセンスと配布

各 provider の source、runtime、model、voice asset、外部 service はそれぞれ異なる条件を持ちます。[Third-party notices](../THIRD_PARTY_NOTICES.md) を確認してください。Koehaku の MIT License は第三者資産を再許諾しません。
