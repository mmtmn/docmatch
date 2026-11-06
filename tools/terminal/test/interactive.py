#!/usr/bin/env python3
"""Exercise the real menu, private profile, multi-file selection, approval and export in a PTY."""
import os
import pty
import subprocess
import fcntl
import termios
import struct
import time
import select
import codecs
import json
import tempfile
import shutil
from pathlib import Path
import pyte
root = Path(__file__).resolve().parents[3]
profile = Path(tempfile.mkdtemp(prefix='docmatch-menu-', dir='/tmp'))
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
env = os.environ | {'DOCMATCH_HOME': str(profile), 'TERM': 'xterm-256color', 'COLORTERM': 'truecolor', 'CI': '0', 'GITHUB_ACTIONS': '0', 'GITLAB_CI': '0', 'BUILDKITE': '0', 'TF_BUILD': '0', 'JENKINS_URL': ''}
env.pop('NO_COLOR', None)
env.pop('DOCMATCH_API_KEY', None)
env.pop('DM_API_BASE_URL', None)

def attach():
    os.setsid()
    fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
p = subprocess.Popen([os.environ.get('DM_AUDIT_TERMINAL', str(root / '.cache/local/bin/docmatch'))], stdin=slave, stdout=slave, stderr=slave, cwd=root / 'testdata/golden/vantara-twin', env=env, preexec_fn=attach)
os.close(slave)
screen = pyte.Screen(120, 40)
stream = pyte.Stream(screen)
decoder = codecs.getincrementaldecoder('utf-8')('replace')
raw = bytearray()
stage = 0
start = time.monotonic()
due = start
try:
    while time.monotonic() - start < 100:
        if select.select([master], [], [], 0.05)[0]:
            try:
                data = os.read(master, 65536)
            except OSError:
                break
            raw.extend(data)
            stream.feed(decoder.decode(data))
            if b'\x1b]11;' in data:
                os.write(master, b'\x1b]11;rgb:1010/1616/1d1d\x1b\\')
        text = '\n'.join(screen.display)
        now = time.monotonic()
        if now < due:
            continue
        if stage == 0 and 'Conectar a uma API' in text:
            os.write(master, b'\x1b[B\x1b[B\r')
            stage = 1
            due = now + 0.3
        elif stage == 1 and 'Endereço da API' in text:
            os.write(master, b'\r')
            stage = 2
            due = now + 0.3
        elif stage == 2 and 'Chave da API' in text:
            os.write(master, b'\r')
            stage = 3
            due = now + 0.3
        elif stage == 3 and 'Abrir documentos nesta pasta' in text:
            os.write(master, b'\r')
            stage = 4
            due = now + 0.3
        elif stage == 4 and 'Pronto para selecionar' in text:
            os.write(master, b'a')
            stage = 5
            due = now + 0.3
        elif stage == 5:
            os.write(master, b'r')
            stage = 6
        elif stage == 6 and 'Aprovar esta' in text:
            os.write(master, b's')
            stage = 7
        elif stage == 7 and 'Concluído' in text:
            os.write(master, b'x')
            stage = 8
        elif stage == 8 and 'Salvo em' in text:
            os.write(master, b'q')
            stage = 9
            due = now + 0.3
        elif stage == 9 and 'Abrir documentos nesta pasta' in text:
            os.write(master, b'\x1b[B' * 6 + b'\r')
            stage = 10
        if p.poll() is not None:
            break
finally:
    if p.poll() is None:
        p.terminate()
    p.wait(timeout=5)
    os.close(master)
(root / 'scratch/audit').mkdir(parents=True, exist_ok=True)
(root / 'scratch/audit/menu.ansi').write_bytes(raw)
(root / 'scratch/audit/menu-screen.txt').write_text('\n'.join(screen.display))
report = {'menu_configure_process_export': stage == 10, 'exit_code': p.returncode, 'profile_private': (profile / 'connection.json').stat().st_mode & 0o777 == 0o600 if (profile / 'connection.json').exists() else False, 'stage': stage}
(root / 'scratch/audit/menu.json').write_text(json.dumps(report, indent=2))
print(json.dumps(report))
shutil.rmtree(profile)
raise SystemExit(0 if stage == 10 and p.returncode == 0 else 1)
