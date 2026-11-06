"""This example approves the quote. Run only when you accept its cap."""
import asyncio,json,os,sys
from pathlib import Path
from docmatch import AsyncClient, Client
async def main():
 c=AsyncClient(os.environ['DM_API_BASE_URL'],os.environ['DOCMATCH_API_KEY']);ids=[]
 for name in sys.argv[1:]:
  p=Path(name);u=await c.upload(p.name,p.read_bytes(),'text/plain' if p.suffix=='.txt' else 'text/csv');ids.append(u['id'])
 input=c.input(ids);quote=await c.estimate(input);print(f"Approved example run: cap {quote['cap_credits']} tokens",file=sys.stderr)
 started=await c.run(quote,input);run=await c.wait(started['id']);findings=await c.findings(run['id']);data=await c.export(run['id'])
 if os.environ.get('DOCMATCH_EXPORT_PATH'):Path(os.environ['DOCMATCH_EXPORT_PATH']).write_bytes(data)
 # Also validate the synchronous client against the same real run.
 sync=Client(os.environ['DM_API_BASE_URL'],os.environ['DOCMATCH_API_KEY']);assert sync.get_run(run['id'])['status']=='completed'
 print(json.dumps({'run':run,'findings':findings}))
asyncio.run(main())
