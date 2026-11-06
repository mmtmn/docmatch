#!/usr/bin/env python3
"""Package real captures with a separate, hash-checked benchmark scorecard.
Requires ffmpeg and Pillow. No application state or processing result is synthesized.
"""
import argparse
import hashlib
import json
import subprocess
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

root = Path(__file__).resolve().parents[2]
scratch = root / 'scratch/master-plan-demo'
dest = root / 'docs/demos'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--only', choices=['website', 'terminal', 'android'], help='Keep the other recordings and their hashes unchanged.')
args = parser.parse_args()


def run(args):
    subprocess.run(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-y', *args], check=True)


def font(size):
    return ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf', size)


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


manifest_path = dest / 'media.json'
manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else {}
for name in ([args.only] if args.only else ['website', 'terminal', 'android']):
    report = json.loads((dest / f'{name}-score.json').read_text())
    result = dest / 'results' / f'{name}.json'
    assert report['passed'] and report['score_percent'] == 100
    assert report['result_sha256'] == sha256(result)
    assert report['expected_sha256'] == sha256(root / 'testdata/golden/vantara-twin/expected.yaml')
    width, height = (720, 1600) if name == 'android' else (1280, 800)
    background, foreground, muted, accent = (
        ('#fcfcfd', '#1b1f24', '#4b5563', '#1d4ed8') if name == 'android'
        else ('#10161d', '#d9e1eb', '#a8b6c9', '#a8c6ff')
    )
    card = Image.new('RGB', (width, height), background)
    draw = ImageDraw.Draw(card)

    def line(text, y, size, color=foreground):
        draw.text((width // 2, y), text, font=font(size), fill=color, anchor='mt')

    line('Verificação do gabarito', height * .16, 36 if name == 'android' else 38)
    line('Vantara · gêmeo público sintético', height * .24, 27 if name == 'android' else 26, muted)
    line('100%', height * .34, 132 if name == 'android' else 98, accent)
    labels = {'bank': 'movimentos bancários', 'titles': 'títulos', 'findings': 'achados e evidências', 'totals': 'totais do documento'}
    for index, (key, label) in enumerate(labels.items()):
        value = report['sections'][key]
        line(f"{value['ok']}/{value['total']}  {label}", height * .55 + index * (56 if name == 'android' else 42), 29 if name == 'android' else 26)
    line('Captura real · OpenRouter ativo', height * .83, 27 if name == 'android' else 24, muted)
    line('Nota deste caso, não de qualquer documento.', height * .90, 23 if name == 'android' else 22, muted)
    cardpath = scratch / f'{name}-scorecard.png'
    card.save(cardpath)

    clips = None
    wait_note = None
    edits = {}
    if name == 'website':
        files = list((scratch / 'web-video').glob('*.webm'))
        assert len(files) == 1
        raw = files[0]
        seconds = float(subprocess.check_output(['ffprobe', '-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', str(raw)], text=True))
        inputs = ['-i', str(raw)]
    elif name == 'terminal':
        meta = json.loads((scratch / 'terminal-recording.json').read_text())
        assert meta['completed']
        seconds = meta['frames'] / meta['fps']
        inputs = ['-framerate', str(meta['fps']), '-i', str(scratch / 'terminal-frames/%05d.png')]
    else:
        meta = json.loads((scratch / 'android-recording.json').read_text())
        assert meta['passed'] and meta['run_id'] == report['run_id'] and meta['download_verified']
        assert report['model_advice_present']
        actual = json.loads(result.read_text())
        assert all(f['details']['model_review']['cached'] is False for f in actual['findings'])
        start, end = meta['start_seconds'], meta['start_seconds'] + meta['duration_seconds']
        processing, ready = meta['scenes']['processing'], meta['scenes']['results']
        assert start < processing < ready < end
        clips = [(start, end)]
        # Keep both ends of a long real wait. Show the cut explicitly, at normal playback speed.
        if ready - processing > 12:
            clips = [(start, processing + 5), (ready - 3, end)]
            wait_note = (processing - start + .5, processing - start + 8)
            edits = {
                'processing_wait_original_seconds': round(ready - processing, 3),
                'processing_wait_shown_seconds': 8,
                'processing_wait_label': 'Espera de processamento abreviada',
            }
        seconds = sum(b - a for a, b in clips)
        inputs = ['-i', str(scratch / 'android-raw.mp4')]

    scale = f'scale={width}:{height}:force_original_aspect_ratio=decrease,pad={width}:{height}:(ow-iw)/2:(oh-ih)/2:color={background.replace("#", "0x")},setsar=1,fps=24,format=yuv420p'
    filters = []
    if clips:
        for index, (a, b) in enumerate(clips):
            filters.append(f'[0:v]trim=start={a}:end={b},setpts=PTS-STARTPTS,{scale}[clip{index}]')
        filters.append(''.join(f'[clip{i}]' for i in range(len(clips))) + f'concat=n={len(clips)}:v=1:a=0[real]')
    else:
        filters.append(f'[0:v]trim=duration={seconds},setpts=PTS-STARTPTS,{scale}[real]')
    footage = 'real'
    if wait_note:
        a, b = wait_note
        filters.append(f"[real]drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf:text='Espera de processamento abreviada':fontsize=26:fontcolor=0x4b5563:box=1:boxcolor=0xfcfcfd@0.95:boxborderw=14:x=(w-tw)/2:y=h-210:enable='between(t,{a},{b})'[noted]")
        footage = 'noted'
    card_seconds = 5 if name == 'android' else 4
    filters.append(f'[1:v]trim=duration={card_seconds},setpts=PTS-STARTPTS,{scale}[card]')
    filters.append(f'[{footage}][card]concat=n=2:v=1:a=0[out]')
    mp4, gif = dest / f'{name}.mp4', dest / f'{name}.gif'
    run([*inputs, '-loop', '1', '-i', str(cardpath), '-filter_complex', ';'.join(filters), '-map', '[out]', '-an', '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '23', '-movflags', '+faststart', str(mp4)])
    gif_width, gif_fps, speed = (432, 12, 1) if name == 'android' else (720, 7, 3)
    run(['-i', str(mp4), '-filter_complex', f'setpts=PTS/{speed},fps={gif_fps},scale={gif_width}:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff:max_colors=128[p];[b][p]paletteuse=dither=bayer:bayer_scale=4', '-loop', '0', str(gif)])
    manifest[name] = {
        'run_id': report['run_id'], 'score': f'{name}-score.json',
        'mp4_sha256': sha256(mp4), 'gif_sha256': sha256(gif),
        'gif_speed': speed, 'scorecard_seconds': card_seconds,
        **({'duration_seconds': round(seconds + card_seconds, 3), **edits} if name == 'android' else {}),
    }
    print(name, 'packaged', flush=True)
manifest_path.write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + '\n')
