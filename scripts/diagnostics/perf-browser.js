import {KoehakuClient} from '../../sdk/typescript/dist/index.js';

const status = document.querySelector('#status');
const query = new URLSearchParams(location.search);
const turns = Number(query.get('turns') || 10);
const betweenTurnsMs = Number(query.get('between_ms') || 500);
const wavURLs = (query.get('wavs') || query.get('wav') || '/runtime/diagnostic-input.wav').split(',').filter(Boolean);
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const result = {started_at: new Date().toISOString(), turns: [], worklet: [], events: []};
window.__perfResult = result;
const save = () => { status.textContent = JSON.stringify(result, null, 2); };
const mark = (type, data = {}) => ({type, client_ms: performance.now(), ...data});

function wavPCM(buffer) {
  const view = new DataView(buffer); let pos = 12;
  while (pos + 8 <= view.byteLength) {
    const id = String.fromCharCode(...new Uint8Array(buffer, pos, 4));
    const size = view.getUint32(pos + 4, true);
    if (id === 'data') return new Uint8Array(buffer, pos + 8, size);
    pos += 8 + size + (size & 1);
  }
  throw new Error('WAV data chunk not found');
}

try {
  const fixtures = await Promise.all(wavURLs.map(async url => ({url, pcm: wavPCM(await (await fetch(url)).arrayBuffer())})));
  const context = new AudioContext();
  await context.audioWorklet.addModule('/sdk/typescript/dist/audio-worklet.js');
  await context.resume();
  const client = new KoehakuClient({baseUrl: 'http://127.0.0.1:8765'});
  const session = await client.realtime.connect();
  const node = new AudioWorkletNode(context, 'pcm-player', {numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [1]});
  node.connect(context.destination);
  let current = null, generationDone = false, playbackDone = false;
  session.on('generationStarted', generation => {
    current = result.turns.at(-1); current.generation_id = generation.id;
    node.port.postMessage({type: 'generation', generationId: generation.id});
  });
  session.on('audio', packet => {
    current.first_browser_pcm_ms ??= performance.now() - current.speech_end_at;
    const samples = new Float32Array(packet.pcm.length / 2), view = new DataView(packet.pcm.buffer, packet.pcm.byteOffset, packet.pcm.byteLength);
    for (let i = 0; i < samples.length; i++) samples[i] = view.getInt16(i * 2, true) / 32768;
    const d = packet.metadata;
    node.port.postMessage({type: 'audio', generationId: packet.generationId, samples, sourceRate: d.sample_rate,
      sourceFrames: d.source_frames, sourceStartFrame: d.source_start_frame, speechSequence: d.speech_sequence,
      audioSequence: d.audio_sequence}, [samples.buffer]);
  });
  session.on('event', event => {
    const now = performance.now(); result.events.push({turn: result.turns.length, type: event.type, client_ms: now, timestamp: event.timestamp,
      data: ['error', 'generation.cancelled', 'generation.done', 'speculation.promoted', 'speculation.invalidated'].includes(event.type) ? event.data : undefined});
    if (!current) return;
    if (event.type === 'input_audio.transcript.partial') {
      current.partial_revisions = (current.partial_revisions || 0) + 1;
      current.first_partial_ms ??= now - current.speech_start_at;
    }
    if (event.type === 'speculation.promoted') current.speculation = 'promoted';
    if (event.type === 'speculation.invalidated') current.speculation = 'mismatched';
    if (event.type === 'input_audio.transcript.final') current.final_asr_ms = now - current.speech_end_at;
    if (event.type === 'response.text.delta' && current.first_llm_ms == null) current.first_llm_ms = now - current.speech_end_at;
    if (event.type === 'response.audio.chunk.started' && current.first_tts_ms == null) {
      current.first_tts_ms = now - current.speech_end_at;
      node.port.postMessage({type: 'format', generationId: event.generation_id, sourceRate: event.data.sample_rate});
    }
    if (event.type === 'generation.done' && event.data.source_frames !== undefined) {
      generationDone = true; current.generation_done_ms = now - current.speech_end_at;
      node.port.postMessage({type: 'done', generationId: event.generation_id, sourceFrames: event.data.source_frames});
    }
    if (event.type === 'generation.cancelled') {
      generationDone = playbackDone = true; current.cancelled = true;
      node.port.postMessage({type: 'clear', generationId: event.generation_id});
    }
  });
  session.on('error', error => { if (current) current.error = {code: error.code, message: error.message}; });
  node.port.onmessage = ({data: m}) => {
    if (!current || m.generationId !== current.generation_id) return;
    const now = performance.now();
    result.worklet.push({turn: current.turn, client_ms: now, ...m});
    if (m.type === 'playback.progress') session.ackRendered(m.playedSourceFrames, m.generationId);
    if (m.type === 'playback.completed' || m.type === 'playback.paused')
      session.ackRendered(m.playedSourceFrames, m.generationId, true);
    if (m.playback_start_latency_ms != null && current.first_render_ms == null)
      current.first_render_ms = now - current.speech_end_at;
    if (m.type === 'playback.completed') {
      playbackDone = true; current.playback_complete_ms = now - current.speech_end_at;
      current.underruns = m.underrun_count; current.max_buffered_ms = m.max_buffered_ms;
      current.playback_duration_ms = m.generation_playback_duration_ms;
    }
  };
  session.startAudioInput(); await sleep(100);
  for (let turn = 1; turn <= turns; turn++) {
    const fixture = fixtures[(turn - 1) % fixtures.length], pcm = fixture.pcm;
    generationDone = playbackDone = false;
    current = {turn, fixture: fixture.url, speech_duration_ms: pcm.length / 32}; result.turns.push(current);
    current.speech_start_at = performance.now(); session.sendEvent({type: 'input_audio.speech_start'});
    for (let p = 0; p < pcm.length; p += 640) { await session.sendAudio(pcm.slice(p, p + 640)); await sleep(20); }
    current.speech_end_at = performance.now(); session.sendEvent({type: 'input_audio.speech_end'});
    const deadline = performance.now() + 120000;
    while ((!generationDone || !playbackDone) && performance.now() < deadline) await sleep(20);
    if (!generationDone || !playbackDone) throw new Error(`turn ${turn} timeout generation=${generationDone} playback=${playbackDone}`);
    current.completed_at = performance.now(); save(); await sleep(betweenTurnsMs);
  }
  await session.close(); node.disconnect(); await context.close();
  result.finished_at = new Date().toISOString(); result.done = true; save();
} catch (error) {
  result.error = {message: error?.message || String(error), stack: error?.stack}; save();
}
