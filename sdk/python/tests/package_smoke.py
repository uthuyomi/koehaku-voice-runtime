"""Build/install a wheel into an isolated target, then verify exports and CLI metadata."""
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[3]
with tempfile.TemporaryDirectory(prefix="koehaku-wheel-") as temporary:
    directory = Path(temporary)
    subprocess.run([sys.executable, "-m", "pip", "wheel", "--no-deps", str(root / "sdk/python"), "--wheel-dir", str(directory)], check=True)
    wheel = next(directory.glob("*.whl"))
    target = directory / "installed"
    subprocess.run([sys.executable, "-m", "pip", "install", "--no-deps", "--target", str(target), str(wheel)], check=True)
    code = '''import sys
sys.path.insert(0, sys.argv[1])
from pathlib import Path
import koehaku_realtime as sdk
import yukkuri_realtime as compatibility_sdk
from importlib.metadata import distribution
assert Path(sdk.__file__).is_relative_to(Path(sys.argv[1]))
assert sdk.PROTOCOL_VERSION == '1'
assert sdk.KoehakuClient is compatibility_sdk.YukkuriClient
entries = {e.name for e in distribution('koehaku-voice-runtime').entry_points}
assert {'koehaku', 'yukkuri'} <= entries
from koehaku_realtime.cli import parser
assert parser().parse_args(['health']).command == 'health'
assert Path(sdk.__file__).with_name('py.typed').exists()
print('Installed wheel SDK/CLI/typing passed')
'''
    subprocess.run([sys.executable, "-c", code, str(target)], check=True)
