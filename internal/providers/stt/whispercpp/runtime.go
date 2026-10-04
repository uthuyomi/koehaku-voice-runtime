package whispercpp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/uthuyomi/koehaku-voice-runtime/internal/providers/stt"
)

type Measurement struct {
	AudioMS             float64  `json:"audio_ms"`
	QueueWaitMS         float64  `json:"runtime_wait_ms"`
	RequestMS           float64  `json:"request_ms"`
	WAVPrepareMS        *float64 `json:"wav_prepare_ms"`
	InferenceMS         *float64 `json:"inference_ms"`
	ParseMS             *float64 `json:"parse_ms"`
	RestartMS           *float64 `json:"restart_ms"`
	Restarted           bool     `json:"restarted"`
	RuntimeReadyMS      *float64 `json:"runtime_ready_ms"`
	ProbeMS             *float64 `json:"probe_ms"`
	LoadMS              *float64 `json:"model_load_ms"`
	EncodeMS            *float64 `json:"encode_ms"`
	DecodeMS            *float64 `json:"decode_ms"`
	ProcessLaunchMS     *float64 `json:"process_launch_api_ms"`
	PeakWorkingSetBytes *uint64  `json:"peak_working_set_bytes"`
	CPUSeconds          *float64 `json:"process_cpu_seconds"`
	Cancelled           bool     `json:"cancelled"`
}

type worker interface {
	infer(context.Context, stt.Request) (*stt.Result, error)
	close()
	measurement() Measurement
	alive() bool
}
type workerFactory func(context.Context, RuntimeConfig, string) (worker, error)

// Runtime owns one process/model/context and serializes all inference. The
// queue is bounded even when used outside the transport admission wrapper.
type Runtime struct {
	cfg         RuntimeConfig
	mu          sync.Mutex
	info        stt.RuntimeInfo
	last        Measurement
	worker      worker // guarded by slot; Close cancels root before waiting for slot
	factory     workerFactory
	slot        chan struct{}
	queue       chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	closeDone   chan struct{}
	initialized bool
}

func NewRuntime(ctx context.Context, c RuntimeConfig) (*Runtime, error) {
	return newRuntime(ctx, c, startWorker)
}
func newRuntime(ctx context.Context, c RuntimeConfig, f workerFactory) (*Runtime, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	p := &Runtime{cfg: c, factory: f, slot: make(chan struct{}, 1), queue: make(chan struct{}, c.QueueCapacity), ctx: root, cancel: cancel, closeDone: make(chan struct{}),
		info: stt.RuntimeInfo{Backend: "whisper.cpp", RequestedDevice: c.Device, Model: c.ModelName, Persistent: c.Mode == "persistent", Concurrency: 1, QueueCapacity: c.QueueCapacity, State: "initializing"}}
	err := p.initialize(ctx, c.Device == "auto")
	p.mu.Lock()
	p.initialized = err == nil
	p.info.Available = err == nil
	if err != nil {
		p.info.State = "unavailable"
	}
	p.mu.Unlock()
	return p, err
}
func (p *Runtime) Name() string                 { return "whisper.cpp" }
func (p *Runtime) RuntimeInfo() stt.RuntimeInfo { p.mu.Lock(); defer p.mu.Unlock(); return p.info }
func (p *Runtime) Measurement() Measurement     { p.mu.Lock(); defer p.mu.Unlock(); return p.last }
func (p *Runtime) initialize(ctx context.Context, auto bool) error {
	device := p.cfg.Device
	if device == "auto" {
		device = "cuda"
	}
	p.mu.Lock()
	if p.info.FallbackFrom == "cuda" {
		device = "cpu"
	}
	p.mu.Unlock()
	attempt := func(device string) (worker, error) {
		bounded, stop := context.WithTimeout(ctx, p.cfg.InitTimeout)
		defer stop()
		return p.factory(bounded, p.cfg, device)
	}
	w, err := attempt(device)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil && w != nil {
		w.close()
		w = nil
	}
	if err != nil && auto && device == "cuda" && ctx.Err() == nil {
		p.mu.Lock()
		p.info.FallbackFrom = "cuda"
		p.info.FallbackReason = "cuda_initialization_failed"
		p.mu.Unlock()
		device = "cpu"
		w, err = attempt(device)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil && w != nil {
			w.close()
			w = nil
		}
	}
	if err != nil {
		p.mu.Lock()
		p.info.Available = false
		p.info.State = "unavailable"
		p.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return stt.ErrUnavailable
	}
	p.worker = w
	p.mu.Lock()
	p.info.SelectedDevice = device
	p.info.Available = true
	p.info.State = "ready"
	p.last = w.measurement()
	info := p.info
	p.mu.Unlock()
	log.Printf("STT initialized: requested_device=%s selected_device=%s model=%s persistent=%v language=%s threads=%d best_of=%d beam_size=%d fallback_from=%s fallback_reason=%s", info.RequestedDevice, info.SelectedDevice, info.Model, info.Persistent, p.cfg.Language, p.cfg.Threads, p.cfg.BestOf, p.cfg.BeamSize, info.FallbackFrom, info.FallbackReason)
	return nil
}

func (p *Runtime) Transcribe(ctx context.Context, r stt.Request) (result *stt.Result, err error) {
	if err = validateRequest(r); err != nil {
		return nil, stt.ErrRuntime
	}
	if r.Format.SampleRate != 16000 || len(r.Audio) > MaxRuntimeAudioBytes {
		return nil, stt.ErrRuntime
	}
	p.mu.Lock()
	unavailable := p.closed || !p.initialized
	p.mu.Unlock()
	if unavailable {
		return nil, stt.ErrUnavailable
	}
	select {
	case p.queue <- struct{}{}:
	default:
		return nil, stt.ErrCapacity
	}
	defer func() { <-p.queue }()
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, p.cfg.InferenceTimeout)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	select {
	case p.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.slot }()
	// AfterFunc cancellation is asynchronous. A queued request can acquire the
	// slot before its callback runs during shutdown; never reload/infer then.
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, context.Canceled
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	waitMS := float64(time.Since(started).Microseconds()) / 1000
	restarted := false
	var restartMS *float64
	if p.worker == nil || !p.worker.alive() {
		if p.worker != nil {
			p.worker.close()
			p.worker = nil
			p.noteCrash()
		}
		restartStarted := time.Now()
		if err = p.initialize(ctx, p.cfg.Device == "auto"); err != nil {
			return nil, err
		}
		ms := float64(time.Since(restartStarted).Microseconds()) / 1000
		restarted = true
		restartMS = &ms
	}
	inferenceStart := time.Now()
	result, err = p.worker.infer(ctx, r)
	metrics := p.worker.measurement()
	metrics.AudioMS = float64(len(r.Audio)) / 32
	metrics.QueueWaitMS = waitMS
	metrics.RequestMS = float64(time.Since(inferenceStart).Microseconds()) / 1000
	metrics.Restarted = restarted
	metrics.RestartMS = restartMS
	if ctx.Err() != nil {
		err = ctx.Err()
		metrics.Cancelled = true
		result = nil
	}
	if err != nil {
		// Some distributed server versions cannot prove that a disconnected
		// inference has released its context. Kill/reap before releasing the slot;
		// the next request reloads one model. Never queue behind obsolete work.
		p.worker.close()
		p.worker = nil
		p.mu.Lock()
		p.info.State = "reload_required"
		p.mu.Unlock()
		if !metrics.Cancelled {
			p.noteCrash()
			if !errors.Is(err, stt.ErrUnavailable) {
				err = stt.ErrRuntime
			}
		}
	}
	p.mu.Lock()
	p.last = metrics
	selected := p.info.SelectedDevice
	p.mu.Unlock()
	log.Printf("STT runtime: selected_device=%s audio_ms=%.2f runtime_wait_ms=%.2f wav_prepare_ms=%s inference_ms=%s parse_ms=%s request_ms=%.2f restarted=%v restart_ms=%s cancelled=%v failed=%v", selected, metrics.AudioMS, metrics.QueueWaitMS, metricValue(metrics.WAVPrepareMS), metricValue(metrics.InferenceMS), metricValue(metrics.ParseMS), metrics.RequestMS, metrics.Restarted, metricValue(metrics.RestartMS), metrics.Cancelled, err != nil)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func metricValue(v *float64) string {
	if v == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.2f", *v)
}
func (p *Runtime) noteCrash() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cfg.Device == "auto" && p.info.SelectedDevice == "cuda" {
		p.info.FallbackFrom = "cuda"
		p.info.FallbackReason = "cuda_runtime_failed"
	}
	p.info.State = "reload_required"
}
func (p *Runtime) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.closeDone
		return nil
	}
	p.closed = true
	p.info.Available = false
	p.info.State = "closed"
	p.mu.Unlock()
	defer close(p.closeDone)
	p.cancel()
	p.slot <- struct{}{}
	defer func() { <-p.slot }()
	if p.worker != nil {
		p.worker.close()
		p.worker = nil
	}
	p.mu.Lock()
	p.info.State = "closed"
	p.info.Available = false
	p.mu.Unlock()
	return nil
}
