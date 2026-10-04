# Third-party notices and operator responsibilities

The repository's [MIT License](LICENSE) covers project-authored code and documentation. It does not relicense third-party software, models, voices, dictionaries, or services.

| Component | How it is used | Distribution policy |
|---|---|---|
| AquesTalk / AqKanji2Koe | Local Japanese TTS and text normalization | Proprietary assets are not included. Operators obtain the software and necessary license from AQUEST. |
| [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) / ONNX Runtime | Nemotron streaming inference worker | sherpa-onnx declares Apache-2.0. Built artifacts are local; preserve its and ONNX Runtime's applicable notices when distributing binaries. |
| [NVIDIA Nemotron 3.5 ASR Streaming 0.6B](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) | Optional streaming STT model | Model files are not included. The current model card identifies OpenMDW 1.1; review the exact downloaded revision and terms before use or redistribution. |
| [whisper.cpp](https://github.com/ggml-org/whisper.cpp) | Existing Whisper STT runtime | The runtime source declares MIT. Binaries and models are not included; separately review the selected model's terms. |
| Smart Turn v3.2 | Local turn-detection sidecar | See [`tools/turn-detector/SMART-TURN-LICENSE`](tools/turn-detector/SMART-TURN-LICENSE). Model/runtime acquisition remains local. |
| OpenAI-compatible LLM | Optional external conversation provider | Credentials are not included. Service terms and data handling are the operator's responsibility. |

This inventory is an engineering audit, not legal advice. Before a public binary or model distribution, the owner should confirm the exact versions, transitive notices, model terms, commercial-use rights, and AQUEST license that apply to that distribution.
