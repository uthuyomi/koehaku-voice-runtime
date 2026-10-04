import {writeFile} from 'node:fs/promises';
import {KoehakuClient} from '../../../sdk/typescript/dist/index.js';
const client = new KoehakuClient({baseUrl: process.env.KOEHAKU_ENGINE_URL});
const result = await client.speak('ゆっくりしていってね');
await writeFile('hello.wav', result.audio);
console.log(result.format, result.requestId);
