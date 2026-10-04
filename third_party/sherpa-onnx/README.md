# sherpa-onnx integration source

Koehaku's Nemotron provider requires a small source integration against sherpa-onnx v1.13.8 (`sherpa-onnx` tag/release 1.13.8):

1. Copy `koehaku-streaming-asr-worker.cc` into `sherpa-onnx/csrc/`.
2. Apply `session-no-spinning.patch` at the sherpa-onnx repository root. This adds the opt-in `SHERPA_ONNX_ORT_DISABLE_SPINNING` session setting; upstream behavior is unchanged when the variable is absent.
3. Add the target shown in `CMakeLists.snippet.txt` to the csrc CMake file.
4. Configure sherpa-onnx with C API and online recognition enabled, then build the `sherpa-onnx-nemotron-worker` Release target.

The engine sets `SHERPA_ONNX_ORT_DISABLE_SPINNING=1` only on this child process. Model files and built binaries are local dependencies and are excluded from this repository. The adjacent development checkout may contain microphone diagnostics; those are not required and are not copied here.

Review and preserve the sherpa-onnx and ONNX Runtime notices when distributing a built worker. See `THIRD_PARTY_NOTICES.md`.
