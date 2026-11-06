import unittest, hashlib, hmac, asyncio
from unittest.mock import patch
import httpx
from docmatch import Client, AsyncClient, DocmatchError, verify_webhook
class Tests(unittest.TestCase):
 def test_webhook(self):
  body=b'{}';header='t=1234567890,v1='+hmac.new(b'secret',b'1234567890.'+body,hashlib.sha256).hexdigest()
  self.assertTrue(verify_webhook(body,header,'secret',1234567890))
  self.assertFalse(verify_webhook(b'changed',header,'secret',1234567890))
  self.assertFalse(verify_webhook(body,header,'secret',1234568191))
  self.assertFalse(verify_webhook(body,'t=1234567890,v1='+('á'*64),'secret',1234567890))
 def test_no_retry_unsafe_post(self):
  with patch.object(httpx.Client,'request',return_value=httpx.Response(503,json={})) as request:
   with self.assertRaises(DocmatchError): Client('https://api.example.invalid','fixture').request('POST','/v1/uploads',{})
   self.assertEqual(request.call_count,1)
 def test_stable_key(self):
  with patch.object(httpx.Client,'request',side_effect=[httpx.Response(429,json={},headers={'Retry-After':'0'}),httpx.Response(200,json={'id':'run'})]) as request:
   Client('https://api.example.invalid','fixture').run({'id':'q','signature':'s'},{})
   self.assertEqual(request.call_args_list[0].kwargs['headers']['Idempotency-Key'],request.call_args_list[1].kwargs['headers']['Idempotency-Key'])
 def test_async_cancel(self):
  async def check():
   client=AsyncClient('https://api.example.invalid','fixture')
   with patch.object(client,'get_run',return_value={'id':'run','status':'running'}):
    task=asyncio.create_task(client.wait('run'));await asyncio.sleep(.01);task.cancel()
    with self.assertRaises(asyncio.CancelledError): await task
  asyncio.run(check())

class ProjectTests(unittest.TestCase):
 def test_scoped_retry_and_unchanged_result(self):
  unchanged={'state':'unchanged','revision':4,'run':{'id':'run_saved','status':'completed'}}
  with patch.object(httpx.Client,'request',side_effect=[httpx.Response(503,json={},headers={'Retry-After':'0'}),httpx.Response(200,json=unchanged)]) as request:
   client=Client('https://api.example.invalid','fixture')
   result=client.project('prj_one').reconcile(4)
   self.assertEqual(result,unchanged)
   self.assertEqual(request.call_count,2)
   first,second=request.call_args_list
   self.assertEqual(first.args[1],'https://api.example.invalid/v1/projects/prj_one/runs')
   self.assertEqual(first.kwargs['headers']['Idempotency-Key'],second.kwargs['headers']['Idempotency-Key'])
   self.assertNotIn('quote_id',second.kwargs['json'])
   self.assertEqual(second.kwargs['json']['lane'],'api_batch')
 def test_findings_are_scoped_and_preserve_cursor(self):
  with patch.object(httpx.Client,'request',side_effect=[httpx.Response(200,json={'items':[{'id':'one'}],'next_cursor':'a+b/='}),httpx.Response(200,json={'items':[{'id':'two'}],'next_cursor':None})]) as request:
   self.assertEqual(len(Client('https://api.example.invalid','fixture').project('prj_one').findings('run_one')),2)
   self.assertIn('/projects/prj_one/runs/run_one/findings',request.call_args.args[1])
   self.assertIn('a%2Bb%2F%3D',request.call_args.args[1])
 def test_async_scope_and_denial_details(self):
  async def check():
   client=AsyncClient('https://api.example.invalid','fixture')
   with patch.object(httpx.AsyncClient,'request',return_value=httpx.Response(403,json={'code':'forbidden','message':'Sem acesso','details':{'reason':'membership_removed'}})) as request:
    with self.assertRaises(DocmatchError) as caught: await client.project('prj_one').get()
    self.assertEqual(caught.exception.details,{'reason':'membership_removed'})
    self.assertEqual(request.call_args.args[1],'https://api.example.invalid/v1/projects/prj_one')
  asyncio.run(check())
