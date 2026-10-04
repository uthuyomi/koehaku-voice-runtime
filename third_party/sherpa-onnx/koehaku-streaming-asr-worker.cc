// Persistent host-PCM worker for Koehaku Voice Runtime.
// The wire format is uint32 little-endian payload length followed by:
// request: type(u8), id(u64), PCM bytes for write/transcribe
// response: type(u8), id(u64), revision(u64), token_count(u32),
//           processed_samples(u64), queued_samples(u64), text_len(u32), text

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
#endif

#include <atomic>
#include <algorithm>
#include <chrono>
#include <condition_variable>
#include <cstdio>
#include <cstdlib>
#include <cstdint>
#include <cstring>
#include <deque>
#include <iostream>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <unordered_map>
#include <utility>
#include <vector>

#include "sherpa-onnx/csrc/online-recognizer.h"
#include "sherpa-onnx/csrc/parse-options.h"

namespace {

constexpr uint8_t kStart = 1, kPcm = 2, kFinalize = 3, kCancel = 4,
                  kTranscribe = 5, kClose = 6;
constexpr uint8_t kStarted = 101, kPartial = 102, kFinal = 103,
                  kError = 104, kReady = 105;
constexpr size_t kMaxFrame = 20 * 1024 * 1024;
constexpr uint64_t kMaxQueuedSamples = 16000 * 10;
constexpr uint64_t kMaxTranscribeSamples = 16000 * 120;

uint32_t Get32(const uint8_t *p) {
  return static_cast<uint32_t>(p[0]) | (static_cast<uint32_t>(p[1]) << 8) |
         (static_cast<uint32_t>(p[2]) << 16) |
         (static_cast<uint32_t>(p[3]) << 24);
}
uint64_t Get64(const uint8_t *p) {
  uint64_t v = 0;
  for (int i = 7; i >= 0; --i) v = (v << 8) | p[i];
  return v;
}
void Put32(std::vector<uint8_t> *v, uint32_t x) {
  for (int i = 0; i != 4; ++i) v->push_back((x >> (i * 8)) & 0xff);
}
void Put64(std::vector<uint8_t> *v, uint64_t x) {
  for (int i = 0; i != 8; ++i) v->push_back((x >> (i * 8)) & 0xff);
}

#ifdef _WIN32
bool ReadAll(SOCKET s, void *data, size_t n) {
  auto *p = static_cast<char *>(data);
  while (n != 0) {
    int r = recv(s, p, static_cast<int>(n), 0);
    if (r <= 0) return false;
    p += r;
    n -= r;
  }
  return true;
}
bool WriteAll(SOCKET s, const void *data, size_t n) {
  auto *p = static_cast<const char *>(data);
  while (n != 0) {
    int r = send(s, p, static_cast<int>(n), 0);
    if (r <= 0) return false;
    p += r;
    n -= r;
  }
  return true;
}
#endif

struct Command {
  uint8_t type = 0;
  uint64_t id = 0;
  std::vector<uint8_t> pcm;
  std::chrono::steady_clock::time_point received_at;
  uint64_t speech_end_backlog = 0;
  uint64_t pcm_commands = 1;
};
struct StreamState {
  std::unique_ptr<sherpa_onnx::OnlineStream> stream;
  uint64_t revision = 0;
  uint64_t accepted_samples = 0;
  std::string last_text;
  std::vector<double> decode_ms;
  std::vector<double> scheduling_gap_ms;
  std::chrono::steady_clock::time_point first_audio_at;
  std::chrono::steady_clock::time_point last_decode_end;
  uint64_t accept_calls = 0;
  uint64_t pcm_commands = 0;
};
struct StreamProgress {
  uint64_t accepted = 0;
  uint64_t processed = 0;
  uint64_t queue_high_watermark = 0;
};

class Worker {
 public:
  Worker(SOCKET client, sherpa_onnx::OnlineRecognizer *recognizer,
         std::string language, bool performance, bool performance_detail)
      : client_(client), recognizer_(recognizer), language_(std::move(language)),
        performance_(performance), performance_detail_(performance_detail) {}

  bool Run() {
    Send(kReady, 0, 0, 0, 0, 0, "");
    decoder_ = std::thread([this] { DecodeLoop(); });
    while (!stopping_) {
      uint8_t len_buf[4];
      if (!ReadAll(client_, len_buf, sizeof(len_buf))) break;
      uint32_t len = Get32(len_buf);
      if (len < 9 || len > kMaxFrame) break;
      std::vector<uint8_t> payload(len);
      if (!ReadAll(client_, payload.data(), payload.size())) break;
      Command c;
      c.type = payload[0];
      c.id = Get64(payload.data() + 1);
      c.received_at = std::chrono::steady_clock::now();
      if (payload.size() > 9) c.pcm.assign(payload.begin() + 9, payload.end());
      if (c.type == kClose) {
        stopping_ = true;
        break;
      }
      if (c.type == kPcm || c.type == kTranscribe) {
        if (c.pcm.empty() || (c.pcm.size() & 1)) {
          SendError(c.id, "invalid PCM frame");
          continue;
        }
        std::lock_guard<std::mutex> lock(mu_);
        uint64_t samples = c.pcm.size() / 2;
        const uint64_t limit = c.type == kTranscribe ? kMaxTranscribeSamples
                                                     : kMaxQueuedSamples;
        if (queued_samples_ + samples > limit) {
          SendError(c.id, "PCM queue full");
          continue;
        }
        queued_samples_ += samples;
        queued_by_stream_[c.id] += samples;
        auto &progress = progress_[c.id];
        progress.queue_high_watermark =
            std::max(progress.queue_high_watermark, queued_by_stream_[c.id]);
        queue_.push_back(std::move(c));
        cv_.notify_one();
      } else {
        std::lock_guard<std::mutex> lock(mu_);
        if (c.type == kFinalize) c.speech_end_backlog = BacklogLocked(c.id);
        queue_.push_back(std::move(c));
        cv_.notify_one();
      }
    }
    stopping_ = true;
    cv_.notify_all();
    if (decoder_.joinable()) decoder_.join();
    return true;
  }

 private:
  void DecodeLoop() {
    while (true) {
      Command c;
      uint64_t queued = 0;
      {
        std::unique_lock<std::mutex> lock(mu_);
        cv_.wait(lock, [this] { return stopping_ || !queue_.empty(); });
        if (queue_.empty() && stopping_) return;
        c = std::move(queue_.front());
        queue_.pop_front();
        RemoveQueuedLocked(c);
        // Coalesce only PCM already waiting for the same stream. This adds no
        // timer latency and never crosses finalize/cancel/control ordering.
        while (c.type == kPcm && !queue_.empty() &&
               queue_.front().type == kPcm && queue_.front().id == c.id) {
          Command next = std::move(queue_.front());
          queue_.pop_front();
          RemoveQueuedLocked(next);
          c.pcm.insert(c.pcm.end(), next.pcm.begin(), next.pcm.end());
          ++c.pcm_commands;
        }
        queued = BacklogLocked(c.id);
      }
      Handle(std::move(c), queued);
    }
  }

  void RemoveQueuedLocked(const Command &c) {
    if (c.type != kPcm && c.type != kTranscribe) return;
    uint64_t samples = c.pcm.size() / 2;
    queued_samples_ -= samples;
    auto it = queued_by_stream_.find(c.id);
    if (it != queued_by_stream_.end()) {
      it->second -= samples;
      if (it->second == 0) queued_by_stream_.erase(it);
    }
  }

  uint64_t BacklogLocked(uint64_t id) const {
    uint64_t queued = 0;
    if (auto it = queued_by_stream_.find(id); it != queued_by_stream_.end())
      queued = it->second;
    if (auto it = progress_.find(id); it != progress_.end() &&
        it->second.accepted > it->second.processed)
      queued += it->second.accepted - it->second.processed;
    return queued;
  }

  uint64_t ProcessedSamples(const StreamState *s) const {
    constexpr uint64_t kSamplesPerFeatureFrame = 160;  // 16 kHz, 10 ms fbank
    uint64_t processed = static_cast<uint64_t>(
        std::max(0, s->stream->GetNumProcessedFrames())) *
        kSamplesPerFeatureFrame;
    return std::min(processed, s->accepted_samples);
  }

  void PublishProgress(uint64_t id, const StreamState *s) {
    std::lock_guard<std::mutex> lock(mu_);
    auto &p = progress_[id];
    p.accepted = s->accepted_samples;
    p.processed = ProcessedSamples(s);
    p.queue_high_watermark = std::max(p.queue_high_watermark, BacklogLocked(id));
  }

  uint64_t CurrentBacklog(uint64_t id) {
    std::lock_guard<std::mutex> lock(mu_);
    return BacklogLocked(id);
  }

  std::unique_ptr<sherpa_onnx::OnlineStream> NewStream() {
    auto s = recognizer_->CreateStream();
    if (!language_.empty()) s->SetOption("language", language_);
    return s;
  }
  static std::vector<float> ConvertPcm(const std::vector<uint8_t> &pcm) {
    std::vector<float> samples(pcm.size() / 2);
    for (size_t i = 0; i != samples.size(); ++i) {
      int16_t x = static_cast<int16_t>(static_cast<uint16_t>(pcm[i * 2]) |
                                      (static_cast<uint16_t>(pcm[i * 2 + 1]) << 8));
      samples[i] = x / 32768.0f;
    }
    return samples;
  }
  uint64_t DecodeReady(StreamState *s, uint64_t id, const char *stage) {
    uint64_t calls = 0;
    auto loop_start = std::chrono::steady_clock::now();
    while (recognizer_->IsReady(s->stream.get())) {
      auto call_start = std::chrono::steady_clock::now();
      if (s->last_decode_end.time_since_epoch().count() != 0) {
        s->scheduling_gap_ms.push_back(
            std::chrono::duration<double, std::milli>(call_start -
                                                       s->last_decode_end)
                .count());
      }
      recognizer_->DecodeStream(s->stream.get());
      auto call_end = std::chrono::steady_clock::now();
      auto call_ms = std::chrono::duration<double, std::milli>(
                         call_end - call_start)
                         .count();
      s->decode_ms.push_back(call_ms);
      if (performance_detail_) {
        std::cerr << "Nemotron decode call: id=" << id
                  << " index=" << s->decode_ms.size()
                  << " stage=" << stage << " ms=" << call_ms << '\n';
      }
      s->last_decode_end = call_end;
      ++calls;
      PublishProgress(id, s);
    }
    if (performance_ && std::string(stage) == "finalize") {
      auto loop_ms = std::chrono::duration<double, std::milli>(
                         std::chrono::steady_clock::now() - loop_start)
                         .count();
      std::cerr << "Nemotron remaining decode loop end: id=" << id
                << " calls=" << calls << " ms=" << loop_ms << '\n';
    }
    return calls;
  }
  void Handle(Command c, uint64_t queued) {
    if (c.type == kStart) {
      auto begin = std::chrono::steady_clock::now();
      streams_[c.id] = {NewStream(), 0, 0, "", {}, {}, {}, {}, 0, 0};
      if (performance_) {
        auto ms = std::chrono::duration<double, std::milli>(
                      std::chrono::steady_clock::now() - begin).count();
        std::cerr << "Nemotron stream create: id=" << c.id << " ms=" << ms << '\n';
      }
      Send(kStarted, c.id, 0, 0, 0, queued, "");
      return;
    }
    if (c.type == kCancel) {
      streams_.erase(c.id);
      std::lock_guard<std::mutex> lock(mu_);
      progress_.erase(c.id);
      queued_by_stream_.erase(c.id);
      return;
    }
    if (c.type == kTranscribe) {
      StreamState s{NewStream(), 0, 0, "", {}, {}, {}, {}, 0, 0};
      Feed(c.id, &s, c.pcm, c.pcm_commands, false);
      Finish(c.id, &s, queued);
      return;
    }
    auto it = streams_.find(c.id);
    if (it == streams_.end()) {
      SendError(c.id, "unknown stream");
      return;
    }
    if (c.type == kPcm) {
      Feed(c.id, &it->second, c.pcm, c.pcm_commands, true);
    } else if (c.type == kFinalize) {
      if (performance_) {
        auto wait_ms = std::chrono::duration<double, std::milli>(
                           std::chrono::steady_clock::now() - c.received_at)
                           .count();
        std::cerr << "Nemotron worker finalize receive: id=" << c.id
                  << " queued_samples=" << queued
                  << " speech_end_backlog_samples=" << c.speech_end_backlog
                  << " queue_wait_ms=" << wait_ms << '\n';
      }
      Finish(c.id, &it->second, c.speech_end_backlog);
      streams_.erase(it);
      std::lock_guard<std::mutex> lock(mu_);
      progress_.erase(c.id);
      queued_by_stream_.erase(c.id);
    } else {
      SendError(c.id, "unknown command");
    }
  }
  void Feed(uint64_t id, StreamState *s, const std::vector<uint8_t> &pcm,
            uint64_t pcm_commands, bool partial) {
    auto samples = ConvertPcm(pcm);
    if (s->first_audio_at.time_since_epoch().count() == 0)
      s->first_audio_at = std::chrono::steady_clock::now();
    s->accepted_samples += samples.size();
    ++s->accept_calls;
    s->pcm_commands += pcm_commands;
    s->stream->AcceptWaveform(16000, samples.data(), samples.size());
    PublishProgress(id, s);
    DecodeReady(s, id, "pcm");
    auto r = recognizer_->GetResult(s->stream.get());
    if (partial && r.text != s->last_text) {
      s->last_text = r.text;
      ++s->revision;
      Send(kPartial, id, s->revision,
           static_cast<uint32_t>(r.tokens.size()), ProcessedSamples(s),
           CurrentBacklog(id),
           r.text);
    }
  }
  void PrintPerformance(uint64_t id, StreamState *s) {
    if (!performance_ || s->decode_ms.empty()) return;
    auto values = s->decode_ms;
    std::sort(values.begin(), values.end());
    auto percentile = [&values](double p) {
      size_t i = static_cast<size_t>(p * static_cast<double>(values.size() - 1));
      return values[i];
    };
    std::cerr << "Nemotron decode: id=" << id << " count=" << values.size()
              << " p50_ms=" << percentile(0.50)
              << " p95_ms=" << percentile(0.95)
              << " max_ms=" << values.back() << '\n';
    double active_ms = s->first_audio_at.time_since_epoch().count() == 0
                           ? 0
                           : std::chrono::duration<double, std::milli>(
                                 std::chrono::steady_clock::now() -
                                 s->first_audio_at)
                                 .count();
    double processed_ms = static_cast<double>(ProcessedSamples(s)) / 16.0;
    auto gaps = s->scheduling_gap_ms;
    std::sort(gaps.begin(), gaps.end());
    auto gap_percentile = [&gaps](double p) {
      if (gaps.empty()) return 0.0;
      return gaps[static_cast<size_t>(p * (gaps.size() - 1))];
    };
    std::lock_guard<std::mutex> lock(mu_);
    auto progress = progress_[id];
    std::cerr << "Nemotron throughput: id=" << id
              << " accepted_samples=" << s->accepted_samples
              << " processed_samples=" << ProcessedSamples(s)
              << " processed_audio_ms=" << processed_ms
              << " wall_time_ms=" << active_ms
              << " ratio=" << (active_ms > 0 ? processed_ms / active_ms : 0)
              << " pcm_commands=" << s->pcm_commands
              << " accept_calls=" << s->accept_calls
              << " queue_high_watermark_samples="
              << progress.queue_high_watermark
              << " scheduling_gap_p50_ms=" << gap_percentile(0.50)
              << " scheduling_gap_p95_ms=" << gap_percentile(0.95)
              << " scheduling_gap_max_ms="
              << (gaps.empty() ? 0 : gaps.back()) << '\n';
  }
  void Finish(uint64_t id, StreamState *s, uint64_t queued) {
    auto finalize_start = std::chrono::steady_clock::now();
    if (performance_)
      std::cerr << "Nemotron worker finalize start: id=" << id << '\n';
    std::vector<float> padding(16000, 0.0f);
    s->stream->AcceptWaveform(16000, padding.data(), padding.size());
    auto input_finished_start = std::chrono::steady_clock::now();
    s->stream->InputFinished();
    if (performance_) {
      auto input_finished_ms = std::chrono::duration<double, std::milli>(
                                   std::chrono::steady_clock::now() -
                                   input_finished_start)
                                   .count();
      std::cerr << "Nemotron inputFinished end: id=" << id
                << " ms=" << input_finished_ms << '\n';
    }
    DecodeReady(s, id, "finalize");
    auto result_start = std::chrono::steady_clock::now();
    auto r = recognizer_->GetResult(s->stream.get());
    if (performance_) {
      auto result_ms = std::chrono::duration<double, std::milli>(
                           std::chrono::steady_clock::now() - result_start)
                           .count();
      std::cerr << "Nemotron final result extraction end: id=" << id
                << " ms=" << result_ms << " tokens=" << r.tokens.size()
                << '\n';
    }
    PrintPerformance(id, s);
    ++s->revision;
    auto send_start = std::chrono::steady_clock::now();
    Send(kFinal, id, s->revision, static_cast<uint32_t>(r.tokens.size()),
         ProcessedSamples(s), queued, r.text);
    if (performance_) {
      auto send_ms = std::chrono::duration<double, std::milli>(
                         std::chrono::steady_clock::now() - send_start)
                         .count();
      auto total_ms = std::chrono::duration<double, std::milli>(
                          std::chrono::steady_clock::now() - finalize_start)
                          .count();
      std::cerr << "Nemotron worker finalize response send: id=" << id
                << " send_ms=" << send_ms << " total_ms=" << total_ms
                << '\n';
    }
  }
  void SendError(uint64_t id, const std::string &message) {
    Send(kError, id, 0, 0, 0, 0, message);
  }
  void Send(uint8_t type, uint64_t id, uint64_t revision, uint32_t tokens,
            uint64_t processed, uint64_t queued, const std::string &text) {
    std::vector<uint8_t> payload;
    payload.reserve(41 + text.size());
    payload.push_back(type);
    Put64(&payload, id);
    Put64(&payload, revision);
    Put32(&payload, tokens);
    Put64(&payload, processed);
    Put64(&payload, queued);
    Put32(&payload, static_cast<uint32_t>(text.size()));
    payload.insert(payload.end(), text.begin(), text.end());
    std::vector<uint8_t> frame;
    Put32(&frame, static_cast<uint32_t>(payload.size()));
    frame.insert(frame.end(), payload.begin(), payload.end());
    std::lock_guard<std::mutex> lock(write_mu_);
    if (!WriteAll(client_, frame.data(), frame.size())) stopping_ = true;
  }

  SOCKET client_;
  sherpa_onnx::OnlineRecognizer *recognizer_;
  std::string language_;
  std::atomic<bool> stopping_{false};
  std::mutex mu_, write_mu_;
  std::condition_variable cv_;
  std::deque<Command> queue_;
  uint64_t queued_samples_ = 0;
  std::unordered_map<uint64_t, uint64_t> queued_by_stream_;
  std::unordered_map<uint64_t, StreamProgress> progress_;
  std::unordered_map<uint64_t, StreamState> streams_;
  std::thread decoder_;
  bool performance_ = false;
  bool performance_detail_ = false;
};

}  // namespace

int main(int argc, char *argv[]) {
#ifndef _WIN32
  std::cerr << "This worker currently supports Windows only\n";
  return 1;
#else
  int32_t port = 0;
  std::string language = "auto";
  bool performance = false;
  bool performance_detail = false;
  sherpa_onnx::ParseOptions po("Persistent Nemotron streaming worker");
  sherpa_onnx::OnlineRecognizerConfig config;
  config.Register(&po);
  po.Register("port", &port, "Loopback TCP port");
  po.Register("language", &language, "Per-stream language or auto");
  po.Register("performance", &performance, "Print per-stream decode timing");
  po.Register("performance-detail", &performance_detail,
              "Print every DecodeStream call (diagnostic only)");
  po.Read(argc, argv);
  if (performance) {
    const char *performance_log = std::getenv("NEMOTRON_PERFORMANCE_LOG");
    if (performance_log != nullptr && performance_log[0] != '\0') {
      FILE *ignored = nullptr;
      freopen_s(&ignored, performance_log, "a", stderr);
    }
  }
  if (port < 1 || port > 65535 || !config.Validate()) {
    po.PrintUsage();
    return 1;
  }
  auto load_start = std::chrono::steady_clock::now();
  sherpa_onnx::OnlineRecognizer recognizer(config);
  auto load_ms = std::chrono::duration_cast<std::chrono::milliseconds>(
                     std::chrono::steady_clock::now() - load_start)
                     .count();
  WSADATA data;
  if (WSAStartup(MAKEWORD(2, 2), &data) != 0) return 2;
  SOCKET server = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
  sockaddr_in addr{};
  addr.sin_family = AF_INET;
  addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  addr.sin_port = htons(static_cast<u_short>(port));
  if (server == INVALID_SOCKET || bind(server, reinterpret_cast<sockaddr *>(&addr), sizeof(addr)) != 0 ||
      listen(server, 1) != 0) {
    std::cerr << "worker listen failed\n";
    return 2;
  }
  std::cout << "NEMOTRON_WORKER_READY port=" << port << " model_load_ms="
            << load_ms << std::endl;
  SOCKET client = accept(server, nullptr, nullptr);
  closesocket(server);
  if (client == INVALID_SOCKET) return 2;
  Worker(client, &recognizer, language, performance, performance_detail).Run();
  closesocket(client);
  WSACleanup();
  return 0;
#endif
}
