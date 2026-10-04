#!/usr/bin/env python3
"""Diagnostic-only persistent Nemotron worker soak.

Exercises the production worker protocol without LLM, Smart Turn, TTS, browser,
or playback.  It intentionally leaves all recognizer/model settings identical to
the Engine's Nemotron provider.
"""

from __future__ import annotations

import argparse
import ctypes
import json
import os
import queue
import re
import socket
import statistics
import struct
import subprocess
import sys
import threading
import time
import wave
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MSG_START, MSG_PCM, MSG_FINALIZE, MSG_TRANSCRIBE, MSG_CLOSE = 1, 2, 3, 5, 6
MSG_STARTED, MSG_PARTIAL, MSG_FINAL, MSG_ERROR, MSG_READY = 101, 102, 103, 104, 105


def load_env(path: Path) -> None:
    if not path.is_file():
        return
    for raw in path.read_text(encoding="utf-8-sig").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip().strip('"').strip("'")
        os.environ.setdefault(key.strip(), value)


def required_path(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is not configured")
    path = Path(value)
    if not path.is_absolute():
        path = ROOT / path
    if not path.is_file():
        raise RuntimeError(f"{name} does not exist: {path}")
    return str(path.resolve())


def load_pcm(path: Path) -> tuple[bytes, float]:
    with wave.open(str(path), "rb") as wav:
        if (wav.getnchannels(), wav.getsampwidth(), wav.getframerate()) != (1, 2, 16000):
            raise RuntimeError(f"fixture must be mono PCM16 16 kHz: {path}")
        pcm = wav.readframes(wav.getnframes())
        return pcm, wav.getnframes() / 16000.0


def percentile(values: list[float], p: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    return ordered[int(p * (len(ordered) - 1))]


def proc_snapshot(pid: int) -> dict:
    if os.name != "nt":
        return {"available": False}
    from ctypes import wintypes

    class FILETIME(ctypes.Structure):
        _fields_ = [("low", wintypes.DWORD), ("high", wintypes.DWORD)]

    class PMC(ctypes.Structure):
        _fields_ = [
            ("cb", wintypes.DWORD), ("PageFaultCount", wintypes.DWORD),
            ("PeakWorkingSetSize", ctypes.c_size_t), ("WorkingSetSize", ctypes.c_size_t),
            ("QuotaPeakPagedPoolUsage", ctypes.c_size_t), ("QuotaPagedPoolUsage", ctypes.c_size_t),
            ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t), ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
            ("PagefileUsage", ctypes.c_size_t), ("PeakPagefileUsage", ctypes.c_size_t),
            ("PrivateUsage", ctypes.c_size_t),
        ]

    kernel32, psapi = ctypes.windll.kernel32, ctypes.windll.psapi
    handle = kernel32.OpenProcess(0x1000 | 0x0010, False, pid)
    if not handle:
        return {"available": False}
    try:
        pmc = PMC(); pmc.cb = ctypes.sizeof(pmc)
        handles = wintypes.DWORD()
        creation = FILETIME(); exit_time = FILETIME(); kernel = FILETIME(); user = FILETIME()
        ok_mem = psapi.GetProcessMemoryInfo(handle, ctypes.byref(pmc), pmc.cb)
        ok_handles = kernel32.GetProcessHandleCount(handle, ctypes.byref(handles))
        ok_cpu = kernel32.GetProcessTimes(handle, ctypes.byref(creation), ctypes.byref(exit_time), ctypes.byref(kernel), ctypes.byref(user))
        to_int = lambda ft: (ft.high << 32) | ft.low
        threads = sum(1 for t in threading_enumeration() if t == pid)
        return {
            "available": True,
            "working_set_bytes": int(pmc.WorkingSetSize) if ok_mem else None,
            "private_bytes": int(pmc.PrivateUsage) if ok_mem else None,
            "handles": int(handles.value) if ok_handles else None,
            "threads": threads,
            "cpu_seconds": (to_int(kernel) + to_int(user)) / 10_000_000 if ok_cpu else None,
        }
    finally:
        kernel32.CloseHandle(handle)


def threading_enumeration():
    from ctypes import wintypes
    class THREADENTRY32(ctypes.Structure):
        _fields_ = [("dwSize", wintypes.DWORD), ("cntUsage", wintypes.DWORD),
                    ("th32ThreadID", wintypes.DWORD), ("th32OwnerProcessID", wintypes.DWORD),
                    ("tpBasePri", wintypes.LONG), ("tpDeltaPri", wintypes.LONG),
                    ("dwFlags", wintypes.DWORD)]
    kernel32 = ctypes.windll.kernel32
    snap = kernel32.CreateToolhelp32Snapshot(0x00000004, 0)
    if snap == ctypes.c_void_p(-1).value:
        return
    try:
        entry = THREADENTRY32(); entry.dwSize = ctypes.sizeof(entry)
        if kernel32.Thread32First(snap, ctypes.byref(entry)):
            while True:
                yield int(entry.th32OwnerProcessID)
                if not kernel32.Thread32Next(snap, ctypes.byref(entry)):
                    break
    finally:
        kernel32.CloseHandle(snap)


class HardwareSampler:
    """Low-rate Windows counters; unavailable sensors remain explicit nulls."""
    def __init__(self):
        self.samples: list[dict] = []
        self.proc = None
        if os.name != "nt": return
        command = r"""
$ErrorActionPreference='Stop'
$c=@('\Processor Information(*)\% Processor Performance','\Processor Information(*)\% Processor Utility','\Processor Information(*)\Processor Frequency')
Get-Counter -Counter $c -SampleInterval 1 -MaxSamples 3600 | ForEach-Object {
  [pscustomobject]@{timestamp_ms=[DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds(); counters=@($_.CounterSamples | ForEach-Object { [pscustomobject]@{path=$_.Path; value=$_.CookedValue} })} | ConvertTo-Json -Compress -Depth 4
}
"""
        flags = subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0
        self.proc = subprocess.Popen(["powershell", "-NoProfile", "-Command", command], stdout=subprocess.PIPE,
                                     stderr=subprocess.DEVNULL, text=True, encoding="utf-8", errors="replace",
                                     creationflags=flags, bufsize=1)
        self.thread = threading.Thread(target=self._read, daemon=True); self.thread.start()

    def _read(self):
        for line in self.proc.stdout:
            try: self.samples.append(json.loads(line))
            except json.JSONDecodeError: pass

    def mark(self) -> int: return len(self.samples)
    def since(self, mark: int) -> list[dict]: return list(self.samples[mark:])
    def close(self):
        if not self.proc: return
        self.proc.terminate()
        try: self.proc.wait(timeout=3)
        except subprocess.TimeoutExpired: self.proc.kill(); self.proc.wait()


class Worker:
    def __init__(self, detail: bool, provider_config: str | None):
        sock = socket.socket(); sock.bind(("127.0.0.1", 0)); self.port = sock.getsockname()[1]; sock.close()
        args = [required_path("NEMOTRON_WORKER_EXECUTABLE"), f"--port={self.port}",
                f"--encoder={required_path('NEMOTRON_ENCODER')}", f"--decoder={required_path('NEMOTRON_DECODER')}",
                f"--joiner={required_path('NEMOTRON_JOINER')}", f"--tokens={required_path('NEMOTRON_TOKENS')}",
                "--num-threads=2", "--provider=" + (f"cpu:{Path(provider_config).resolve()}" if provider_config else "cpu"), "--decoding-method=greedy_search",
                "--language=auto", "--performance=true"]
        if detail: args.append("--performance-detail=true")
        flags = subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0
        self.proc = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                                     encoding="utf-8", errors="replace", creationflags=flags, bufsize=1)
        ready = self.proc.stdout.readline().strip()
        if not ready.startswith("NEMOTRON_WORKER_READY "):
            raise RuntimeError(f"worker did not become ready: {ready}")
        self.ready = ready
        self.lines: list[str] = []
        self.finished: dict[int, threading.Event] = {}
        self.stderr_thread = threading.Thread(target=self._stderr, daemon=True); self.stderr_thread.start()
        deadline = time.monotonic() + 5
        while True:
            try:
                self.socket = socket.create_connection(("127.0.0.1", self.port), timeout=.25); break
            except OSError:
                if time.monotonic() >= deadline: raise
                time.sleep(.025)
        self.socket.settimeout(180)
        if self.recv()["type"] != MSG_READY: raise RuntimeError("worker protocol did not send ready")

    def _stderr(self):
        for line in self.proc.stderr:
            line = line.rstrip(); self.lines.append(line)
            match = re.search(r"finalize response send: id=(\d+)", line)
            if match: self.finished.setdefault(int(match.group(1)), threading.Event()).set()

    def send(self, kind: int, stream_id: int, pcm: bytes = b""):
        payload = bytes([kind]) + struct.pack("<Q", stream_id) + pcm
        self.socket.sendall(struct.pack("<I", len(payload)) + payload)

    def recv(self) -> dict:
        length = struct.unpack("<I", self._read(4))[0]; data = self._read(length)
        kind, stream_id, revision, tokens, processed, queued, text_len = struct.unpack("<BQQIQQI", data[:41])
        return {"type": kind, "id": stream_id, "revision": revision, "tokens": tokens,
                "processed_samples": processed, "queued_samples": queued,
                "text": data[41:41 + text_len].decode("utf-8", "replace")}

    def _read(self, size: int) -> bytes:
        chunks = bytearray()
        while len(chunks) < size:
            part = self.socket.recv(size - len(chunks))
            if not part: raise RuntimeError("worker connection closed")
            chunks.extend(part)
        return bytes(chunks)

    def run(self, stream_id: int, pcm: bytes, realtime: bool, chunk_ms: int) -> tuple[dict, float, float | None]:
        started = time.perf_counter(); first_partial = None
        if realtime:
            self.send(MSG_START, stream_id)
            while self.recv()["type"] != MSG_STARTED: pass
            chunk_bytes = 16000 * 2 * chunk_ms // 1000
            target = time.perf_counter()
            for offset in range(0, len(pcm), chunk_bytes):
                self.send(MSG_PCM, stream_id, pcm[offset:offset + chunk_bytes])
                target += len(pcm[offset:offset + chunk_bytes]) / 2 / 16000
                delay = target - time.perf_counter()
                if delay > 0: time.sleep(delay)
            finalize_start = time.perf_counter(); self.send(MSG_FINALIZE, stream_id)
        else:
            finalize_start = time.perf_counter(); self.send(MSG_TRANSCRIBE, stream_id, pcm)
        while True:
            result = self.recv()
            if result["id"] != stream_id: continue
            if result["type"] == MSG_PARTIAL and first_partial is None:
                first_partial = (time.perf_counter() - started) * 1000
            if result["type"] == MSG_ERROR: raise RuntimeError(result["text"])
            if result["type"] == MSG_FINAL: break
        result["finalize_ms"] = (time.perf_counter() - finalize_start) * 1000
        result["wall_ms"] = (time.perf_counter() - started) * 1000
        return result, result["wall_ms"], first_partial

    def metrics(self, stream_id: int) -> dict:
        self.finished.setdefault(stream_id, threading.Event()).wait(2)
        lines = [x for x in self.lines if f"id={stream_id} " in x]
        calls = [float(m.group(1)) for x in lines if (m := re.search(r"decode call: .* ms=([0-9.]+)", x))]
        decode = next((x for x in lines if x.startswith("Nemotron decode:")), "")
        throughput = next((x for x in lines if x.startswith("Nemotron throughput:")), "")
        fields = lambda line: {k: float(v) for k, v in re.findall(r"([a-z0-9_]+)=([0-9.]+)", line)}
        return {"decode_calls_ms": calls, "decode": fields(decode), "throughput": fields(throughput),
                "worker_log": lines}

    def close(self):
        try: self.send(MSG_CLOSE, 0)
        except Exception: pass
        try: self.socket.close()
        except Exception: pass
        try: self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired: self.proc.kill(); self.proc.wait()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("realtime", "max-throughput"), required=True)
    parser.add_argument("--turns", type=int, default=100)
    parser.add_argument("--chunk-ms", type=int, default=20)
    parser.add_argument("--baseline-turns", type=int, default=5)
    parser.add_argument("--rolling-turns", type=int, default=10)
    parser.add_argument("--early-stop", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--detail", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--provider-config", help="sherpa/ORT diagnostic config file")
    parser.add_argument("--idle-before", default="", help="diagnostic pauses as utterance:seconds,...")
    parser.add_argument("--output", required=True)
    parser.add_argument("fixtures", nargs="+")
    args = parser.parse_args()
    if args.turns < 1 or args.baseline_turns < 1 or args.rolling_turns < 1: parser.error("turn counts must be positive")
    load_env(ROOT / ".env")
    idle_before = {}
    for item in filter(None, args.idle_before.split(",")):
        utterance, seconds = item.split(":", 1); idle_before[int(utterance)] = float(seconds)
    fixtures = [(str(Path(x).resolve()), *load_pcm(Path(x).resolve())) for x in args.fixtures]
    output = Path(args.output); output.parent.mkdir(parents=True, exist_ok=True)
    hardware = HardwareSampler()
    worker = Worker(args.detail, args.provider_config)
    artifact = {"schema": "nemotron-soak-v1", "mode": args.mode, "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "configuration": {"threads": 2, "language": "auto", "provider": "cpu", "decoding_method": "greedy_search",
                                  "chunk_ms": args.chunk_ms, "early_stop_ratio": 1.30,
                                  "provider_config": str(Path(args.provider_config).resolve()) if args.provider_config else None},
                "worker_ready": worker.ready, "hardware": {"cpu_counters": "Windows Processor Information",
                "temperature": "unavailable", "power": "unavailable", "throttling": "unavailable"},
                "turns": [], "early_stop": None}
    baseline = None
    try:
        for index in range(args.turns):
            idle_seconds = idle_before.get(index + 1, 0)
            if idle_seconds:
                print(f"idle {idle_seconds:.1f}s before utterance {index+1}", flush=True)
                time.sleep(idle_seconds)
            path, pcm, duration = fixtures[index % len(fixtures)]
            hardware_mark = hardware.mark()
            before = proc_snapshot(worker.proc.pid)
            result, wall_ms, first_partial = worker.run(index + 1, pcm, args.mode == "realtime", args.chunk_ms)
            detail = worker.metrics(index + 1); after = proc_snapshot(worker.proc.pid)
            calls = detail["decode_calls_ms"]
            turn = {"utterance_index": index + 1, "fixture": path, "idle_before_seconds": idle_seconds,
                    "audio_duration_ms": duration * 1000,
                    "accepted_samples": len(pcm) // 2, "processed_samples": result["processed_samples"],
                    "queued_samples": result["queued_samples"], "queued_audio_ms": result["queued_samples"] / 16,
                    "first_partial_ms": first_partial, "finalize_ms": result["finalize_ms"], "final_asr_ms": wall_ms,
                    "decode_count": len(calls), "decode_calls_ms": calls, "decode_p50_ms": percentile(calls, .50),
                    "decode_p95_ms": percentile(calls, .95), "decode_max_ms": max(calls, default=0),
                    "scheduling_gap_p50_ms": detail["throughput"].get("scheduling_gap_p50_ms"),
                    "scheduling_gap_p95_ms": detail["throughput"].get("scheduling_gap_p95_ms"),
                    "throughput_ratio": detail["throughput"].get("ratio"), "process_before": before,
                    "process_after": after, "hardware_samples": hardware.since(hardware_mark),
                    "text_length": len(result["text"]), "worker_log": detail["worker_log"]}
            artifact["turns"].append(turn)
            print(f"{index+1:03d} decode_p50={turn['decode_p50_ms']:.1f} p95={turn['decode_p95_ms']:.1f} final={wall_ms:.1f} ms", flush=True)
            completed = len(artifact["turns"])
            if completed == args.baseline_turns:
                sample = artifact["turns"][:args.baseline_turns]
                baseline = {"decode_p50_ms": statistics.mean(x["decode_p50_ms"] for x in sample),
                            "decode_p95_ms": statistics.mean(x["decode_p95_ms"] for x in sample),
                            "final_asr_ms": statistics.mean(x["final_asr_ms"] for x in sample)}
                artifact["baseline"] = baseline
            if args.early_stop and baseline and completed >= args.baseline_turns + args.rolling_turns:
                sample = artifact["turns"][-args.rolling_turns:]
                rolling = {"decode_p50_ms": statistics.mean(x["decode_p50_ms"] for x in sample),
                           "decode_p95_ms": statistics.mean(x["decode_p95_ms"] for x in sample),
                           "final_asr_ms": statistics.mean(x["final_asr_ms"] for x in sample)}
                breached = [key for key in rolling if rolling[key] > baseline[key] * 1.30]
                if breached:
                    artifact["early_stop"] = {"utterance_index": completed, "reason": "DEGRADATION DETECTED",
                                              "metrics": breached, "rolling": rolling}
                    print(f"DEGRADATION DETECTED at utterance {completed}: {', '.join(breached)}", flush=True)
                    break
            output.write_text(json.dumps(artifact, ensure_ascii=False, indent=2), encoding="utf-8")
    finally:
        artifact["finished_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        artifact["worker_final"] = proc_snapshot(worker.proc.pid)
        artifact["hardware_samples"] = hardware.samples
        output.write_text(json.dumps(artifact, ensure_ascii=False, indent=2), encoding="utf-8")
        worker.close()
        hardware.close()
    return 2 if artifact["early_stop"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
