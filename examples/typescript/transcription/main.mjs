import {readFile} from 'node:fs/promises';
import {KoehakuClient, wavToPCM} from '../../../sdk/typescript/dist/index.js';
const client = new KoehakuClient({baseUrl: process.env.KOEHAKU_ENGINE_URL});
const pcm = wavToPCM(await readFile(process.argv[2] ?? 'input.wav'));
console.log((await client.transcribe(pcm)).text);
