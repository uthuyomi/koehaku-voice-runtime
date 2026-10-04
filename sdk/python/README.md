# Koehaku Python SDK

Async Python 3.11+ SDK and `koehaku` CLI for Public API v1. The previous `yukkuri_realtime` import package, `YukkuriClient` / `YukkuriError` names, and `yukkuri` command remain compatibility aliases.

```sh
python -m pip install -e ./sdk/python
koehaku health
koehaku speak "こんにちは" --output hello.wav
```

See [Python SDK](../../docs/python-sdk.md) and [CLI](../../docs/cli.md). No ML dependency, automatic reconnect, implicit speaker playback, or package publication.
