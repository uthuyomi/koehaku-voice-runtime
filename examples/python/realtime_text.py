import asyncio
import os
from koehaku_realtime import KoehakuClient


async def main():
    async with KoehakuClient(os.environ.get("KOEHAKU_ENGINE_URL", "http://127.0.0.1:8765")) as client:
        async with await client.realtime.connect() as session:
            session.on("text_delta", lambda e: print(e["delta"], end="", flush=True))
            generation = await session.send_text("札幌について教えて")
            await generation.wait_done(timeout=130)
            print()


asyncio.run(main())
