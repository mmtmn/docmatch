#!/usr/bin/env python3
"""Package real captures with a separate, hash-checked benchmark scorecard.
Requires ffmpeg and Pillow. No application state or processing result is synthesized.
"""
import hashlib,json,subprocess
from pathlib import Path
from PIL import Image,ImageDraw,ImageFont
root=Path(__file__).resolve().parents[2];scratch=root/'scratch/master-plan-demo';dest=root/'docs/demos'
def run(args):subprocess.run(['ffmpeg','-hide_banner','-loglevel','error','-y',*args],check=True)
def font(size):return ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf',size)
manifest={}
for name in ['website','terminal','android']:
 report=json.loads((dest/f'{name}-score.json').read_text());result=dest/'results'/f'{name}.json'
 assert report['passed'] and report['score_percent']==100
 assert report['result_sha256']==hashlib.sha256(result.read_bytes()).hexdigest()
 assert report['expected_sha256']==hashlib.sha256((root/'testdata/golden/vantara-twin/expected.yaml').read_bytes()).hexdigest()
 width,height=(540,1200) if name=='android' else (1280,800)
 card=Image.new('RGB',(width,height),'#10161d');d=ImageDraw.Draw(card)
 center=width//2
 def line(text,y,size,color='#d9e1eb'):d.text((center,y),text,font=font(size),fill=color,anchor='mt')
 line('Verificação do gabarito',height*.16,26 if name=='android' else 38)
 line('Vantara · gêmeo público sintético',height*.24,20 if name=='android' else 26,'#a8b6c9')
 line('100%',height*.34,98,'#a8c6ff')
 labels={'bank':'movimentos bancários','titles':'títulos','findings':'achados e evidências','totals':'totais do documento'}
 for index,(key,label) in enumerate(labels.items()):
  value=report['sections'][key];line(f"{value['ok']}/{value['total']}  {label}",height*.55+index*42,22 if name=='android' else 26)
 line('Captura real · OpenRouter ativo',height*.83,20 if name=='android' else 24,'#a8b6c9')
 line('Nota deste caso, não de qualquer documento.',height*.90,17 if name=='android' else 22,'#a8b6c9')
 cardpath=scratch/f'{name}-scorecard.png';card.save(cardpath)
 if name=='website':
  files=list((scratch/'web-video').glob('*.webm'));assert len(files)==1;raw=files[0]
  seconds=float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','csv=p=0',str(raw)],text=True));inputs=['-i',str(raw)]
 elif name=='terminal':
  meta=json.loads((scratch/'terminal-recording.json').read_text());assert meta['completed']
  seconds=meta['frames']/meta['fps'];inputs=['-framerate',str(meta['fps']),'-i',str(scratch/'terminal-frames/%05d.png')]
 else:
  meta=json.loads((scratch/'android-recording.json').read_text());assert meta['passed'] and meta['run_id']==report['run_id']
  seconds=meta['duration_seconds'];inputs=['-ss',str(meta['start_seconds']),'-i',str(scratch/'android-raw.mp4')]
 scale=f'scale={width}:{height}:force_original_aspect_ratio=decrease,pad={width}:{height}:(ow-iw)/2:(oh-ih)/2:color=0x10161d,setsar=1,fps=24,format=yuv420p'
 filters=f'[0:v]trim=duration={seconds},setpts=PTS-STARTPTS,{scale}[real];[1:v]trim=duration=4,setpts=PTS-STARTPTS,{scale}[card];[real][card]concat=n=2:v=1:a=0[out]'
 mp4=dest/f'{name}.mp4';gif=dest/f'{name}.gif'
 run([*inputs,'-loop','1','-i',str(cardpath),'-filter_complex',filters,'-map','[out]','-an','-c:v','libx264','-preset','veryfast','-crf','25','-movflags','+faststart',str(mp4)])
 gifwidth=360 if name=='android' else 720
 run(['-i',str(mp4),'-filter_complex',f'setpts=PTS/3,fps=7,scale={gifwidth}:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff:max_colors=96[p];[b][p]paletteuse=dither=bayer:bayer_scale=4','-loop','0',str(gif)])
 manifest[name]={'run_id':report['run_id'],'score':f'{name}-score.json','mp4_sha256':hashlib.sha256(mp4.read_bytes()).hexdigest(),'gif_sha256':hashlib.sha256(gif.read_bytes()).hexdigest(),'gif_speed':3,'scorecard_seconds':4}
 print(name,'packaged',flush=True)
(dest/'media.json').write_text(json.dumps(manifest,indent=2)+'\n')
