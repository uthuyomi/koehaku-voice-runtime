package nemotron

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/uthuyomi/yukkuri-realtime-engine/internal/providers/stt"
)

const (
	msgStart      byte = 1
	msgPCM        byte = 2
	msgFinalize   byte = 3
	msgCancel     byte = 4
	msgTranscribe byte = 5
	msgClose      byte = 6
	msgStarted    byte = 101
	msgPartial    byte = 102
	msgFinal      byte = 103
	msgError      byte = 104
	msgReady      byte = 105
	maxFrame           = 20 << 20
)

type wireResult struct {
	typeID                          byte
	id, revision, processed, queued uint64
	tokens                          int
	text                            string
}

type Provider struct {
	cfg     Config
	ctx     context.Context
	cancel  context.CancelFunc
	cmd     *exec.Cmd
	conn    net.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	streams map[uint64]*stream
	next    atomic.Uint64
	epoch   uint64
	closed  bool
	done    chan struct{}
}

type stream struct {
	p         *Provider
	id, epoch uint64
	ctx       context.Context
	cancel    context.CancelFunc
	results   chan stt.StreamResult
	final     chan finalResponse
	write     chan []byte
	done      chan struct{}
	once      sync.Once
	streaming bool
}
type finalResponse struct {
	result *stt.Result
	err    error
}

func New(ctx context.Context, cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &Provider{cfg: cfg, ctx: pctx, cancel: cancel, streams: map[uint64]*stream{}, epoch: uint64(time.Now().UnixNano()), done: make(chan struct{})}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	args := []string{
		"--port=" + strconv.Itoa(port), "--encoder=" + cfg.Encoder,
		"--decoder=" + cfg.Decoder, "--joiner=" + cfg.Joiner, "--tokens=" + cfg.Tokens,
		"--num-threads=" + strconv.Itoa(cfg.Threads), "--provider=cpu",
		"--decoding-method=greedy_search", "--language=" + cfg.Language,
	}
	if cfg.Performance {
		args = append(args, "--performance=true")
	}
	p.cmd = exec.CommandContext(pctx, cfg.WorkerExecutable, args...)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	p.cmd.Stderr = os.Stderr
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	startedAt := time.Now()
	if err = p.cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start Nemotron worker: %w", err)
	}
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "NEMOTRON_WORKER_READY ") {
				log.Printf("%s startup_ms=%.2f", scanner.Text(), float64(time.Since(startedAt).Microseconds())/1000)
				ready <- nil
				return
			}
		}
		ready <- fmt.Errorf("Nemotron worker exited before ready")
	}()
	select {
	case err = <-ready:
	case <-time.After(2 * time.Minute):
		err = fmt.Errorf("Nemotron worker startup timeout")
	case <-pctx.Done():
		err = pctx.Err()
	}
	if err != nil {
		p.Close()
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.conn, err = net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("connect Nemotron worker: %w", err)
	}
	go p.readLoop()
	return p, nil
}

func (p *Provider) Name() string { return "nemotron" }
func (p *Provider) RuntimeInfo() stt.RuntimeInfo {
	p.mu.Lock()
	available := !p.closed
	p.mu.Unlock()
	return stt.RuntimeInfo{Backend: "sherpa-onnx", RequestedDevice: "cpu", SelectedDevice: "cpu", Model: "nemotron-3.5-asr-streaming-0.6b-560ms-int8", Persistent: true, Available: available, State: map[bool]string{true: "ready", false: "closed"}[available], Concurrency: 1, QueueCapacity: 8}
}

func (p *Provider) Transcribe(ctx context.Context, req stt.Request) (*stt.Result, error) {
	if req.Format != (stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}) || len(req.Audio) == 0 || len(req.Audio)%2 != 0 {
		return nil, fmt.Errorf("invalid Nemotron audio")
	}
	id := p.next.Add(1)
	s := p.newStream(ctx, id)
	p.register(s)
	defer p.unregister(id)
	if err := p.send(msgTranscribe, id, req.Audio); err != nil {
		return nil, err
	}
	select {
	case r := <-s.final:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, stt.ErrUnavailable
	}
}

func (p *Provider) StartStream(ctx context.Context, req stt.StreamRequest) (stt.Stream, error) {
	if req.Format != (stt.AudioFormat{SampleRate: 16000, Channels: 1, Encoding: "pcm_s16le"}) {
		return nil, fmt.Errorf("invalid Nemotron stream format")
	}
	id := p.next.Add(1)
	s := p.newStream(ctx, id)
	s.streaming = true
	p.register(s)
	go s.writeLoop()
	if err := p.send(msgStart, id, nil); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (p *Provider) newStream(ctx context.Context, id uint64) *stream {
	if ctx == nil {
		ctx = p.ctx
	}
	c, cancel := context.WithCancel(ctx)
	s := &stream{p: p, id: id, epoch: p.epoch, ctx: c, cancel: cancel, results: make(chan stt.StreamResult, 16), final: make(chan finalResponse, 1), write: make(chan []byte, p.cfg.QueueChunks), done: make(chan struct{})}
	context.AfterFunc(c, func() { s.close(true) })
	return s
}
func (p *Provider) register(s *stream)   { p.mu.Lock(); p.streams[s.id] = s; p.mu.Unlock() }
func (p *Provider) unregister(id uint64) { p.mu.Lock(); delete(p.streams, id); p.mu.Unlock() }

func (s *stream) WritePCM(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	if len(pcm)%2 != 0 || len(pcm) > maxFrame-9 {
		return fmt.Errorf("invalid PCM frame")
	}
	b := append([]byte(nil), pcm...)
	select {
	case s.write <- b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return context.Canceled
	default:
		return stt.ErrCapacity
	}
}
func (s *stream) Results() <-chan stt.StreamResult { return s.results }
func (s *stream) Finalize(ctx context.Context) (*stt.Result, error) {
	started := time.Now()
	if s.p.cfg.Performance {
		log.Printf("Nemotron finalize enqueue: stream=%d epoch=%d", s.id, s.epoch)
	}
	select {
	case s.write <- nil:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, context.Canceled
	}
	select {
	case r := <-s.final:
		if s.p.cfg.Performance {
			log.Printf("Nemotron finalize completed: stream=%d epoch=%d duration_ms=%.2f err=%v", s.id, s.epoch, float64(time.Since(started).Microseconds())/1000, r.err)
		}
		return r.result, r.err
	case <-ctx.Done():
		if s.p.cfg.Performance {
			log.Printf("Nemotron finalize context ended: stream=%d epoch=%d duration_ms=%.2f err=%v", s.id, s.epoch, float64(time.Since(started).Microseconds())/1000, ctx.Err())
		}
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, context.Canceled
	}
}
func (s *stream) Cancel()      { s.close(true) }
func (s *stream) Close() error { s.close(true); return nil }
func (s *stream) close(notify bool) {
	s.once.Do(func() {
		if notify {
			_ = s.p.send(msgCancel, s.id, nil)
		}
		s.cancel()
		close(s.done)
		s.p.unregister(s.id)
	})
}
func (s *stream) writeLoop() {
	defer func() { recover() }()
	for {
		select {
		case pcm := <-s.write:
			if pcm == nil {
				started := time.Now()
				if s.p.cfg.Performance {
					log.Printf("Nemotron finalize request send start: stream=%d epoch=%d", s.id, s.epoch)
				}
				err := s.p.send(msgFinalize, s.id, nil)
				if s.p.cfg.Performance {
					log.Printf("Nemotron finalize request send end: stream=%d epoch=%d duration_ms=%.2f err=%v", s.id, s.epoch, float64(time.Since(started).Microseconds())/1000, err)
				}
				if err != nil {
					s.deliverError(err)
				}
				return
			}
			if err := s.p.send(msgPCM, s.id, pcm); err != nil {
				s.deliverError(err)
				return
			}
		case <-s.done:
			return
		case <-s.ctx.Done():
			return
		}
	}
}
func (s *stream) deliverError(err error) {
	select {
	case s.final <- finalResponse{err: err}:
	default:
	}
	s.close(false)
}

func (p *Provider) send(kind byte, id uint64, data []byte) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || p.conn == nil {
		return stt.ErrUnavailable
	}
	payload := make([]byte, 9+len(data))
	payload[0] = kind
	binary.LittleEndian.PutUint64(payload[1:9], id)
	copy(payload[9:], data)
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.conn.Write(frame)
	if err != nil {
		return stt.ErrRuntime
	}
	return nil
}
func (p *Provider) readLoop() {
	defer p.failAll(stt.ErrUnavailable)
	for {
		var n uint32
		if binary.Read(p.conn, binary.LittleEndian, &n) != nil {
			return
		}
		if n < 41 || n > maxFrame {
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(p.conn, b); err != nil {
			return
		}
		wr := wireResult{typeID: b[0], id: binary.LittleEndian.Uint64(b[1:9]), revision: binary.LittleEndian.Uint64(b[9:17]), tokens: int(binary.LittleEndian.Uint32(b[17:21])), processed: binary.LittleEndian.Uint64(b[21:29]), queued: binary.LittleEndian.Uint64(b[29:37])}
		l := int(binary.LittleEndian.Uint32(b[37:41]))
		if l < 0 || 41+l != len(b) {
			return
		}
		wr.text = string(b[41:])
		if wr.typeID == msgReady {
			continue
		}
		p.dispatch(wr)
	}
}
func (p *Provider) dispatch(w wireResult) {
	p.mu.Lock()
	s := p.streams[w.id]
	p.mu.Unlock()
	if s == nil || s.epoch != p.epoch {
		return
	}
	if w.typeID == msgError {
		s.deliverError(errors.New(w.text))
		return
	}
	result := stt.Result{Text: w.text}
	if w.typeID == msgFinal {
		if p.cfg.Performance {
			log.Printf("Nemotron Engine finalize response receive: stream=%d epoch=%d revision=%d tokens=%d processed_samples=%d queued_samples=%d", s.id, s.epoch, w.revision, w.tokens, w.processed, w.queued)
		}
		if s.streaming {
			select {
			case s.results <- stt.StreamResult{Result: result, StreamID: s.id, Epoch: s.epoch, Revision: w.revision, TokenCount: w.tokens, ProcessedSamples: w.processed, QueuedSamples: w.queued, WorkerLag: time.Duration(w.queued) * time.Second / 16000, Final: true}:
			case <-s.ctx.Done():
			}
		}
		select {
		case s.final <- finalResponse{result: &result}:
		default:
		}
		return
	}
	if w.typeID == msgPartial {
		select {
		case s.results <- stt.StreamResult{Result: result, StreamID: s.id, Epoch: s.epoch, Revision: w.revision, TokenCount: w.tokens, ProcessedSamples: w.processed, QueuedSamples: w.queued, WorkerLag: time.Duration(w.queued) * time.Second / 16000}:
		default:
		}
	}
}
func (p *Provider) failAll(err error) {
	p.mu.Lock()
	list := make([]*stream, 0, len(p.streams))
	for _, s := range p.streams {
		list = append(list, s)
	}
	p.closed = true
	p.mu.Unlock()
	for _, s := range list {
		s.deliverError(err)
	}
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}
func (p *Provider) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	p.cancel()
	if p.conn != nil {
		_ = p.send(msgClose, 0, nil)
		_ = p.conn.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}
