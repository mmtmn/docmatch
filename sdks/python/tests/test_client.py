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
