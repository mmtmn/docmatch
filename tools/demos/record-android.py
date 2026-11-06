#!/usr/bin/env python3
"""Record an emulator-only APK test. Refuse test APKs containing non-public fixtures."""
import json,os,re,signal,subprocess,time,zipfile
from pathlib import Path
root=Path(__file__).resolve().parents[2];out=root/'scratch/master-plan-demo';out.mkdir(parents=True,exist_ok=True)
adb=os.environ.get('ADB','adb');serial=os.environ.get('ANDROID_SERIAL','emulator-5556')
if not serial.startswith('emulator-'):raise SystemExit('Use an isolated emulator, not a personal phone.')
apk=root/'apps/android/app/build/outputs/apk/debug/app-debug.apk';test=root/'apps/android/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk'
pack=root/'testdata/golden/vantara-twin'
with zipfile.ZipFile(test) as archive:
 for name in ['01_contas_a_receber_protheus_jul2026.csv','02_titulos_unidade_norte_omie_jul2026.csv','03_extrato_bancario_jul2026.csv','04_caixa_de_entrada_contas_a_pagar.txt','expected.yaml']:
  if archive.read('assets/'+name)!=(pack/name).read_bytes():raise SystemExit('APK fixtures do not match the public twin; recording refused.')
def run(*args,**kwargs):return subprocess.run([adb,'-s',serial,*args],check=True,**kwargs)
for path in [apk,test]:run('install','-r',str(path),stdout=subprocess.DEVNULL)
run('shell','pm','clear','br.com.docmatch.debug',stdout=subprocess.DEVNULL);run('logcat','-c')
started=time.monotonic();remote='/sdcard/docmatch-final-public.mp4'
record=subprocess.Popen([adb,'-s',serial,'shell','screenrecord','--bit-rate','2500000','--time-limit','150',remote],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
time.sleep(1);offset=time.monotonic()-started
try:
 with (out/'android-test.log').open('w') as log:
  result=run('shell','am','instrument','-w','-r','-e','class','br.com.docmatch.app.LocalFlowTest#recordPublicTwinDemo','-e','docmatchLocal','1','-e','docmatchRecord','1','-e','docmatchRequireModel','1','br.com.docmatch.debug.test/androidx.test.runner.AndroidJUnitRunner',stdout=log,stderr=subprocess.STDOUT,timeout=140)
 end=time.monotonic()-started
finally:
 subprocess.run([adb,'-s',serial,'shell','pkill','-2','screenrecord'],check=False,stdout=subprocess.DEVNULL)
 try:record.wait(timeout=15)
 except subprocess.TimeoutExpired:record.terminate();record.wait(timeout=5)
run('pull',remote,str(out/'android-raw.mp4'),stdout=subprocess.DEVNULL)
log=run('logcat','-d','-s','DOCMATCH_DEMO',capture_output=True,text=True).stdout
(out/'android-log.txt').write_text(log)
if 'OK (1 test)' not in (out/'android-test.log').read_text():raise SystemExit('Android demo test failed.')
id=re.findall(r'completed=(run_[A-Z0-9]+)',log)[-1]
(out/'android-recording.json').write_text(json.dumps({'passed':True,'run_id':id,'start_seconds':offset,'duration_seconds':end-offset-1,'fixture':'public-twin'},indent=2)+'\n')
print('Android APK recording passed:',id)
