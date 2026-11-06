#!/usr/bin/env python3
"""Record a real APK workflow on an isolated emulator with public fixtures only."""
import http.cookiejar
import json
import os
import re
import subprocess
import time
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

root = Path(__file__).resolve().parents[2]
out = root / 'scratch/master-plan-demo'
out.mkdir(parents=True, exist_ok=True)
adb = os.environ.get('ADB', 'adb')
serial = os.environ.get('ANDROID_SERIAL', 'emulator-5556')
api = os.environ.get('DM_DEMO_API', 'http://localhost:18080').rstrip('/')
if not serial.startswith('emulator-'):
    raise SystemExit('Use an isolated emulator, not a personal phone.')
if urllib.parse.urlparse(api).hostname not in ('localhost', '127.0.0.1'):
    raise SystemExit('Recording requires a local development API.')

apk = root / 'apps/android/app/build/outputs/apk/debug/app-debug.apk'
test = root / 'apps/android/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk'
pack = root / 'testdata/golden/vantara-twin'
with zipfile.ZipFile(test) as archive:
    for name in [
        '01_contas_a_receber_protheus_jul2026.csv',
        '02_titulos_unidade_norte_omie_jul2026.csv',
        '03_extrato_bancario_jul2026.csv',
        '04_caixa_de_entrada_contas_a_pagar.txt',
        'expected.yaml',
    ]:
        if archive.read('assets/' + name) != (pack / name).read_bytes():
            raise SystemExit('APK fixtures do not match the public twin; recording refused.')


def run(*args, **kwargs):
    return subprocess.run([adb, '-s', serial, *args], check=True, **kwargs)


for path in [apk, test]:
    run('install', '-r', str(path), stdout=subprocess.DEVNULL)
run('shell', 'pm', 'clear', 'br.com.docmatch.debug', stdout=subprocess.DEVNULL)
run('logcat', '-c')
started = float(run('shell', 'cat', '/proc/uptime', capture_output=True, text=True).stdout.split()[0])
remote = '/sdcard/docmatch-public-demo.mp4'
record = subprocess.Popen(
    [adb, '-s', serial, 'shell', 'screenrecord', '--bit-rate', '6000000', '--time-limit', '180', remote],
    stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
)
time.sleep(1)
try:
    with (out / 'android-test.log').open('w') as log:
        run(
            'shell', 'am', 'instrument', '-w', '-r',
            '-e', 'class', 'br.com.docmatch.app.LocalFlowTest#recordPublicTwinDemo',
            '-e', 'docmatchLocal', '1', '-e', 'docmatchRecord', '1',
            '-e', 'docmatchRequireModel', '1',
            'br.com.docmatch.debug.test/androidx.test.runner.AndroidJUnitRunner',
            stdout=log, stderr=subprocess.STDOUT, timeout=170,
        )
finally:
    subprocess.run([adb, '-s', serial, 'shell', 'pkill', '-2', 'screenrecord'], check=False, stdout=subprocess.DEVNULL)
    try:
        record.wait(timeout=15)
    except subprocess.TimeoutExpired:
        record.terminate()
        record.wait(timeout=5)
run('pull', remote, str(out / 'android-raw.mp4'), stdout=subprocess.DEVNULL)
log = run('logcat', '-d', '-s', 'DOCMATCH_DEMO', capture_output=True, text=True).stdout
(out / 'android-log.txt').write_text(log)
if 'OK (1 test)' not in (out / 'android-test.log').read_text():
    raise SystemExit('Android demo test failed; see scratch/master-plan-demo/android-test.log.')
run_id = re.findall(r'completed=(run_[A-Z0-9]+)', log)[-1]
scenes = {name: round(int(ms) / 1000 - started, 3) for name, ms in re.findall(r'scene=(\w+) uptime_ms=(\d+)', log)}
start = round(max(0, scenes['login'] - 0.2), 3)
end = scenes['finish'] + 2.3
(out / 'android-recording.json').write_text(json.dumps({
    'passed': True, 'run_id': run_id, 'start_seconds': start,
    'duration_seconds': round(end - start, 3), 'scenes': scenes,
    'fixture': 'public-twin', 'download_verified': True,
}, indent=2) + '\n')

# Fetch the same dev account's real run for the separate golden-data verifier.
# Session cookies stay in memory; they never enter the recording or result file.
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
login = urllib.request.Request(api + '/v1/auth/dev/login', data=b'{}', headers={'Content-Type': 'application/json'})
with client.open(login, timeout=30) as response:
    response.read()
try:
    with client.open(api + '/v1/runs/' + run_id, timeout=30) as response:
        actual_run = json.load(response)
    with client.open(api + '/v1/runs/' + run_id + '/findings?limit=100', timeout=30) as response:
        findings = json.load(response)
    assert not findings.get('next_cursor'), 'Capture every finding before scoring.'
    (out / 'android-run.json').write_text(json.dumps({'run': actual_run, 'findings': findings['items']}, indent=2, ensure_ascii=False) + '\n')
finally:
    with client.open(urllib.request.Request(api + '/v1/auth/logout', data=b'{}', headers={'Content-Type': 'application/json'}), timeout=30) as response:
        response.read()
print('Android APK recording and corrected XLSX download passed:', run_id)
