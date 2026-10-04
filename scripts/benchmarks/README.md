# Benchmark tools

These tools compare STT providers through the public transcription API.

1. Generate the local corpus:

   ```powershell
   ./scripts/benchmarks/generate-corpus.ps1
   ```

2. Start one provider with `scripts/dev.ps1`. Keep the intended model, thread, language, and decoder configuration visible in the report.

3. Run the harness:

   ```powershell
   py -3.12 scripts/benchmarks/stt-accuracy.py `
     --provider nemotron `
     --output benchmark-results/local-nemotron.json
   ```

   Repeat with `--provider whisper` against a Whisper launcher. `--repeat` controls latency repetitions; the first repetition per sample supplies CER/WER.

The manifest stores references and synthesis voices. WAV files are generated under ignored `runtime/benchmark-corpus/` and must not be committed. The JSON artifact includes provider capabilities, every transcript, per-request latency and RTF, and aggregate Japanese CER / English WER.

See [`docs/benchmarks/methodology.md`](../../docs/benchmarks/methodology.md) before interpreting or publishing results.
