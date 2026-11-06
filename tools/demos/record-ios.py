#!/usr/bin/env python3
"""Record the real iOS UI on a dedicated simulator against the local API.

Requires Xcode, xcodegen, a booted simulator named 'docmatch ...', and make local.
The four public inputs are copied into Files; no responses or app screens are mocked.
"""
import argparse
import json
import plistlib
from pathlib import Path
import signal
import subprocess
import time
from urllib.parse import urlparse
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "scratch/ios-demo"
INPUTS = [
    "01_contas_a_receber_protheus_jul2026.csv",
    "02_titulos_unidade_norte_omie_jul2026.csv",
    "03_extrato_bancario_jul2026.csv",
    "04_caixa_de_entrada_contas_a_pagar.txt",
]


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--simulator", required=True)
    parser.add_argument("--api", default="http://localhost:18080")
    args = parser.parse_args()
    url = urlparse(args.api)
    if url.scheme != "http" or url.hostname not in {"localhost", "127.0.0.1"} or url.username or url.query or url.fragment or url.path:
        parser.error("use a local HTTP API origin, without credentials or a path")
    with urlopen(args.api + "/v1/auth/methods", timeout=10) as response:
        if not json.load(response).get("dev"):
            parser.error("the API must enable local development login")
    devices = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "devices", "--json"]))["devices"]
    device = next((d for group in devices.values() for d in group if d["udid"] == args.simulator), None)
    if not device or device["state"] != "Booted" or not device["name"].lower().startswith("docmatch "):
        parser.error("use a booted, dedicated simulator named 'docmatch ...'")
    groups = Path.home() / "Library/Developer/CoreSimulator/Devices" / args.simulator / "data/Containers/Shared/AppGroup"
    providers = []
    for metadata in groups.glob("*/.com.apple.mobile_container_manager.metadata.plist"):
        if plistlib.loads(metadata.read_bytes()).get("MCMMetadataIdentifier") == "group.com.apple.FileProvider.LocalStorage":
            providers.append(metadata.parent / "File Provider Storage/docmatch-public-twin")
    if len(providers) != 1:
        parser.error("open Files once on the simulator to initialize its local storage")
    folder = providers[0]
    folder.mkdir(parents=True, exist_ok=True)
    if any(p.name not in INPUTS for p in folder.iterdir()):
        parser.error("the public demo folder contains unexpected files")
    for name in INPUTS:
        source = ROOT / "testdata/golden/vantara-twin" / name
        target = folder / name
        if target.exists() and target.read_bytes() != source.read_bytes():
            parser.error("the public demo folder contains a modified input")
        target.write_bytes(source.read_bytes())
    OUT.mkdir(parents=True, exist_ok=True)
    project = ROOT / "apps/ios"
    run("xcodegen", "generate", "--quiet", cwd=project)
    command = ["xcodebuild", "-project", "Docmatch.xcodeproj", "-scheme", "Docmatch",
               "-configuration", "Debug", "-destination", "platform=iOS Simulator,id=" + args.simulator,
               "-derivedDataPath", str(ROOT / ".cache/ios-demo"), "DOCMATCH_API_BASE_URL=" + args.api,
               "CODE_SIGNING_ALLOWED=NO", "-parallel-testing-enabled", "NO",
               "-only-testing:DocmatchUITests/DemoTests/testPublicTwinRecording"]
    with (OUT / "build.log").open("w") as log:
        run(*command, "build-for-testing", cwd=project, stdout=log, stderr=subprocess.STDOUT)
    raw = OUT / "ios-raw.mp4"
    if raw.exists():
        parser.error("archive scratch/ios-demo/ios-raw.mp4 before recording again")
    started = time.monotonic()
    started_at = time.time()
    with (OUT / "capture.log").open("w") as capture_log:
        capture = subprocess.Popen(["xcrun", "simctl", "io", args.simulator, "recordVideo", "--codec=h264", str(raw)],
                                   stdout=capture_log, stderr=subprocess.STDOUT)
        try:
            time.sleep(2)
            if capture.poll() is not None:
                raise RuntimeError("simulator recording did not start; see capture.log")
            with (OUT / "test.log").open("w") as log:
                run(*command, "test-without-building", cwd=project, stdout=log, stderr=subprocess.STDOUT, timeout=600)
        finally:
            if capture.poll() is None:
                capture.send_signal(signal.SIGINT)
            capture.wait(timeout=30)
    (OUT / "recording.json").write_text(json.dumps({
        "passed": True, "simulator": args.simulator, "api": args.api,
        "fixture": "public-twin", "duration_seconds": time.monotonic() - started,
        "started_at": started_at,
        "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
    }, indent=2) + "\n")
    print("Captured real iOS flow. Review and trim it with tools/demos/package-ios.py.")


if __name__ == "__main__":
    main()
