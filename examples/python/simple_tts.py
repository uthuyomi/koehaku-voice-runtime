import asyncio
import os
from pathlib import Path
from koehaku_realtime import KoehakuClient


async def main():
    async with KoehakuClient(os.environ.get("KOEHAKU_ENGINE_URL", "http://127.0.0.1:8765")) as client:
        result = await client.speak("ゆっくりしていってね")
        Path("hello.wav").write_bytes(result.audio)


asyncio.run(main())
