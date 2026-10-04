#!/usr/bin/env python3
"""Provider-neutral public transcription benchmark with CER/WER scoring."""

import argparse, asyncio, json, re, statistics, sys, time, unicodedata, wave
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdk" / "python" / "src"))
from koehaku_realtime import KoehakuClient

def distance(a, b):
    previous = list(range(len(b) + 1))
    for i, x in enumerate(a, 1):
        current = [i]
        for j, y in enumerate(b, 1):
            current.append(min(current[-1] + 1, previous[j] + 1, previous[j-1] + (x != y)))
        previous = current
    return previous[-1]

def normalize_chars(text):
    text = unicodedata.normalize("NFKC", text).lower()
    return [c for c in text if not c.isspace() and not unicodedata.category(c).startswith(("P", "S"))]

def normalize_words(text):
    return re.findall(r"[a-z0-9]+(?:'[a-z0-9]+)?", unicodedata.normalize("NFKC", text).lower())

def pcm(path):
    with wave.open(str(path), "rb") as w:
        if (w.getframerate(), w.getnchannels(), w.getsampwidth()) != (16000, 1, 2):
            raise ValueError(f"not mono PCM16 16 kHz: {path}")
        return w.readframes(w.getnframes()), w.getnframes() / 16000

def summarize(values):
    values = sorted(values)
    return {"mean": statistics.mean(values), "p50": values[int(.50*(len(values)-1))],
            "p95": values[int(.95*(len(values)-1))], "max": values[-1]}

async def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--provider", required=True, choices=("whisper", "nemotron"))
    ap.add_argument("--manifest", default=str(ROOT / "scripts" / "benchmarks" / "corpus-manifest.json"))
    ap.add_argument("--audio-dir", default=str(ROOT / "runtime" / "benchmark-corpus"))
    ap.add_argument("--output", required=True)
    ap.add_argument("--base-url", default="http://127.0.0.1:8765")
    ap.add_argument("--repeat", type=int, default=2)
    args = ap.parse_args()
    manifest = json.loads(Path(args.manifest).read_text(encoding="utf-8")); records=[]
    async with KoehakuClient(base_url=args.base_url) as client:
        capabilities = await client.capabilities()
        for sample in manifest["samples"]:
            audio, seconds = pcm(Path(args.audio_dir) / sample["file"])
            for repeat in range(1, args.repeat + 1):
                started = time.perf_counter(); result = await client.transcribe(audio, timeout=180)
                elapsed = (time.perf_counter() - started) * 1000
                ref_chars, hyp_chars = normalize_chars(sample["reference"]), normalize_chars(result.text)
                ref_words, hyp_words = normalize_words(sample["reference"]), normalize_words(result.text)
                records.append({"id":sample["id"], "language":sample["language"], "repeat":repeat,
                    "audio_seconds":seconds, "latency_ms":elapsed, "rtf":elapsed/(seconds*1000),
                    "reference":sample["reference"], "hypothesis":result.text,
                    "character_errors":distance(ref_chars,hyp_chars), "reference_characters":len(ref_chars),
                    "word_errors":distance(ref_words,hyp_words), "reference_words":len(ref_words)})
    ja=[r for r in records if r["language"]=="ja" and r["repeat"]==1]
    en=[r for r in records if r["language"]=="en" and r["repeat"]==1]
    artifact={"schema":"koehaku-stt-accuracy-v1", "provider":args.provider,
        "methodology":{"endpoint":"WS /v1/transcription", "sample_rate":16000, "language_mode":"provider configuration", "repeat":args.repeat,
                       "latency":"client commit through final transcript", "accuracy":"first repetition only"},
        "capabilities":capabilities, "records":records,
        "summary":{"latency_ms":summarize([r["latency_ms"] for r in records]),
            "japanese_cer":sum(r["character_errors"] for r in ja)/sum(r["reference_characters"] for r in ja),
            "english_wer":sum(r["word_errors"] for r in en)/sum(r["reference_words"] for r in en)}}
    Path(args.output).write_text(json.dumps(artifact,ensure_ascii=False,indent=2),encoding="utf-8")
    print(json.dumps(artifact["summary"],ensure_ascii=False,indent=2))

asyncio.run(main())
