#!/usr/bin/env python3
"""Capture the real Bubble Tea PTY. Render received ANSI cells, never mock UI states.
Requires pyte + Pillow. The recorded documents are always the public synthetic twin.
"""
import codecs,fcntl,io,json,os,pty,select,struct,subprocess,sys,termios,time
from pathlib import Path
import pyte
from PIL import Image,ImageDraw,ImageFont
root=Path(__file__).resolve().parents[2];out=root/'scratch/master-plan-demo';frames=out/'terminal-frames';frames.mkdir(parents=True,exist_ok=True)
columns,rows=108,31;screen=pyte.Screen(columns,rows);stream=pyte.Stream(screen);decoder=codecs.getincrementaldecoder('utf-8')('replace')
font=ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf',16)
symbols=ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf',16)
bold=ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf',16)
cell_w,cell_h=10,21
palette={'default':'#d9e1eb','black':'#111820','red':'#dd9e99','green':'#9fc7ae','brown':'#d5bd90','blue':'#8ab4ff','magenta':'#c5a7d4','cyan':'#9dc5d4','white':'#d9e1eb','brightblack':'#8b949e','brightred':'#efa9a0','brightgreen':'#b3dfbd','brightblue':'#a8c6ff','brightwhite':'#ffffff'}
def color(value,bg=False):
 if value=='default' and bg:return '#10161d'
 if value in palette:return palette[value]
 if len(value)==6 and all(c in 'abcdef0123456789' for c in value.lower()):return '#'+value
 return '#10161d' if bg else '#d9e1eb'
def render(n):
 image=Image.new('RGB',(columns*cell_w+32,rows*cell_h+62),'#10161d');d=ImageDraw.Draw(image)
 d.rounded_rectangle((8,8,image.width-8,image.height-8),radius=16,outline='#344252',width=2)
 for i,c in enumerate(['#bd7c79','#bca477','#82aa91']):d.ellipse((22+i*20,20,31+i*20,29),fill=c)
 d.text((100,12),'docmatch · terminal real · gêmeo público Vantara',font=font,fill='#a8b6c9')
 for y,line in screen.buffer.items():
  for x,char in line.items():
   px,py=16+x*cell_w,44+y*cell_h;fg,bg=color(char.fg),color(char.bg,True)
   if char.reverse:fg,bg=bg,fg
   if bg!='#10161d':d.rectangle((px,py,px+cell_w,py+cell_h),fill=bg)
   if char.data.strip():d.text((px,py),char.data,font=symbols if any(ord(c)>127 for c in char.data) else bold if char.bold else font,fill=fg)
 image.save(frames/f'{n:05d}.png')
master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',rows,columns,0,0))
pack=root/'testdata/golden/vantara-twin'
command=[str(root/'output/docmatch'),'workspace','--api',os.environ.get('DM_DEMO_API','http://localhost:18080'),'--dir',str(pack),'--output',str(out/'terminal-record-export')]
record_env=os.environ|{'TERM':'xterm-256color','COLORTERM':'truecolor','COLORFGBG':'15;0','CI':'0','GITHUB_ACTIONS':'0','GITLAB_CI':'0','BUILDKITE':'0','TF_BUILD':'0','JENKINS_URL':''}
record_env.pop('NO_COLOR',None)
def attach_tty():
 os.setsid();fcntl.ioctl(slave,termios.TIOCSCTTY,0)
process=subprocess.Popen(command,preexec_fn=attach_tty,stdin=slave,stdout=slave,stderr=slave,cwd=root,env=record_env);os.close(slave)
started=time.monotonic();next_frame=started;stage=0;due=started;count=0;raw=bytearray();done_at=None
try:
 while time.monotonic()-started<240:
  now=time.monotonic()
  if select.select([master],[],[],.03)[0]:
   try:data=os.read(master,65536)
   except OSError:break
   if not data:break
   raw.extend(data);stream.feed(decoder.decode(data))
   # Reply to terminal background query if requested by termenv.
   if b'\x1b]11;' in data:os.write(master,b'\x1b]11;rgb:1010/1616/1d1d\x1b\\')
  text='\n'.join(screen.display)
  if now>=next_frame:render(count);count+=1;next_frame=now+.125
  if stage==0 and 'Pronto para selecionar' in text:stage=1;due=now+1.5
  elif stage==1 and now>=due:os.write(master,b'a');stage=2;due=now+1.5
  elif stage==2 and now>=due:os.write(master,b'd\t');stage=3;due=now+1
  elif stage==3 and now>=due:
   names=[p for p in sorted(pack.iterdir()) if p.suffix in ('.csv','.txt') and p.name[:2].isdigit()]
   dropped=' '.join("'"+str(p)+"'" for p in names);os.write(master,b'\x1b[200~'+dropped.encode()+b'\x1b[201~');stage=4;due=now+1.5
  elif stage==4 and now>=due:os.write(master,b'\r');stage=5;due=now+1.5
  elif stage==5 and now>=due:os.write(master,b'r');stage=6
  elif stage==6 and 'Aprovar esta' in text:stage=7;due=now+2
  elif stage==7 and now>=due:os.write(master,b's');stage=8
  elif stage==8 and 'Concluído' in text and 'baixar XLSX' in text:stage=9;due=now+2
  elif stage==9 and now>=due:os.write(master,b'x');stage=10
  elif stage==10 and 'Salvo em' in text:stage=11;due=now+2
  elif stage==11 and now>=due:os.write(master,b'\x1b[B\x1b[B');stage=12;due=now+2
  elif stage==12 and now>=due:os.write(master,b'q');break
  if process.poll() is not None:break
finally:
 if process.poll() is None:
  try:process.wait(timeout=3)
  except subprocess.TimeoutExpired:process.terminate();process.wait(timeout=3)
 os.close(master)
(out/'terminal.ansi').write_bytes(raw)
(out/'terminal-recording.json').write_text(json.dumps({'completed':stage==12,'frames':count,'fps':8,'exit_code':process.returncode},indent=2)+'\n')
if stage!=12:raise SystemExit('Terminal recording did not complete; inspect captured ANSI output.')
print('Recorded',count,'real terminal frames.')
