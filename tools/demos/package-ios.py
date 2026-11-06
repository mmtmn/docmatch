#!/usr/bin/env python3
"""Package a real simulator capture, without generated screens or unverified benchmark claims.

From the repository root, after reviewing the recording and choosing the trim:
  python3 tools/demos/package-ios.py --source scratch/ios-demo/ios-raw.mp4 \
      --start 5 --duration 60

FFMPEG may name an executable; otherwise use imageio_ffmpeg from this interpreter
or .cache/validation/venv. Output: normal-speed ios.mp4, 3x ios.gif, and the ios
entry in docs/demos/media.json. No scorecard is added. An optional --report/--result
pair records a benchmark only after checking its hashes and run ID.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[2]
DEST = ROOT / "docs/demos"


def seconds(value):
    number = float(value)
    if not math.isfinite(number) or number < 0:
        raise argparse.ArgumentTypeError("seconds must be finite and nonnegative")
    return number


def ffmpeg_executable():
    configured = os.environ.get("FFMPEG")
    if configured:
        executable = shutil.which(configured)
        if not executable:
            raise ValueError("FFMPEG must name an executable, without arguments")
        return executable
    try:
        import imageio_ffmpeg
    except ImportError:
        python = ROOT / ".cache/validation/venv/bin/python"
        if not python.is_file():
            raise ValueError("set FFMPEG or install imageio_ffmpeg in .cache/validation/venv")
        return subprocess.check_output(
            [str(python), "-c", "import imageio_ffmpeg; print(imageio_ffmpeg.get_ffmpeg_exe())"],
            text=True,
        ).strip()
    return imageio_ffmpeg.get_ffmpeg_exe()


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_manifest(path):
    # Refuse malformed metadata instead of silently discarding other demos.
    manifest = json.loads(path.read_text())
    if not isinstance(manifest, dict):
        raise ValueError("media.json must contain an object")
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--source", required=True, type=Path, help="real simulator recording")
    parser.add_argument("--start", required=True, type=seconds, help="trim start in seconds")
    parser.add_argument("--duration", required=True, type=seconds, help="trim length in seconds (> 0)")
    parser.add_argument("--report", type=Path, help="optional verified benchmark report")
    parser.add_argument("--result", type=Path, help="API response checked by --report")
    args = parser.parse_args()
    if args.duration == 0:
        parser.error("--duration must be greater than zero")
    if bool(args.report) != bool(args.result):
        parser.error("--report and --result must be supplied together")
    benchmark = None
    if args.report:
        benchmark = json.loads(args.report.read_text())
        result = json.loads(args.result.read_text())
        if not (benchmark.get("passed") and benchmark.get("score_percent") == 100
                and benchmark.get("result_sha256") == sha256(args.result)
                and benchmark.get("expected_sha256") == sha256(ROOT / "testdata/golden/vantara-twin/expected.yaml")
                and benchmark.get("run_id") == result.get("run", {}).get("id")):
            parser.error("benchmark report does not verify this response and public answer key")
    source = args.source.resolve(strict=True)
    if not source.is_file() or source in {(DEST / "ios.mp4").resolve(), (DEST / "ios.gif").resolve()}:
        parser.error("--source must be a recording file distinct from the outputs")
    manifest_path = DEST / "media.json"
    read_manifest(manifest_path)
    ffmpeg = ffmpeg_executable()
    source_hash = sha256(source)

    def run(arguments):
        subprocess.run([ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", *arguments], check=True)

    # Encode both files before replacing any published output.
    with tempfile.TemporaryDirectory(prefix=".ios-package-", dir=DEST) as directory:
        staging = Path(directory)
        mp4, gif = staging / "ios.mp4", staging / "ios.gif"
        run([
            "-ss", str(args.start), "-i", str(source), "-t", str(args.duration),
            "-map", "0:v:0", "-an", "-map_metadata", "-1", "-map_chapters", "-1",
            "-vf", "setpts=PTS-STARTPTS,scale=720:-2,setsar=1,fps=24,format=yuv420p",
            "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-movflags", "+faststart", str(mp4),
        ])
        run([
            "-i", str(mp4), "-filter_complex",
            "[0:v]setpts=(PTS-STARTPTS)/3,fps=10,scale=360:-1:flags=lanczos,split[a][b];"
            "[a]palettegen=stats_mode=diff:max_colors=128[p];"
            "[b][p]paletteuse=dither=bayer:bayer_scale=4[out]",
            "-map", "[out]", "-an", "-loop", "0", str(gif),
        ])
        if not mp4.stat().st_size or not gif.stat().st_size:
            raise ValueError("empty output; check the recording and trim interval")
        if sha256(source) != source_hash:
            raise ValueError("source changed during packaging; wait for recording to finish")
        manifest = read_manifest(manifest_path)
        manifest["ios"] = {
            "kind": "real_simulator_ui_demo",
            "benchmark_verified": False,
            "source_sha256": source_hash,
            "trim_start_seconds": args.start,
            "trim_requested_duration_seconds": args.duration,
            "mp4_sha256": sha256(mp4),
            "gif_sha256": sha256(gif),
            "mp4_speed": 1,
            "gif_speed": 3,
            "scorecard_seconds": 0,
        }
        if benchmark:
            manifest["ios"].update(benchmark_verified=True, run_id=benchmark["run_id"], score=args.report.name)
        metadata = staging / "media.json"
        metadata.write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n")
        mp4.replace(DEST / "ios.mp4")
        gif.replace(DEST / "ios.gif")
        metadata.replace(manifest_path)
    print("Packaged real iOS UI demo: normal-speed MP4, 3x GIF; benchmark verified:", benchmark is not None)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit(f"iOS packaging failed: {error}") from None
