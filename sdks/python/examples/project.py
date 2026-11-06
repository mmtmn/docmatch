"""Inspect a saved project. Approval is explicit and limited; Apache-2.0."""
import os
import sys
from docmatch import Client

client = Client(os.environ["DM_API_BASE_URL"], os.environ["DOCMATCH_API_KEY"])
project = client.project(sys.argv[1])
saved = project.get()
analysis = project.estimate(saved["revision"])
if analysis.get("run"):
    print("Conciliação existente:", analysis["run"]["id"], analysis["run"]["status"])
elif analysis.get("quote"):
    quote = analysis["quote"]
    print(f'{saved["name"]} · limite: {quote["cap_credits"]} créditos')
    approved = int(os.getenv("DOCMATCH_APPROVE_CAP", "0"))
    if approved > 0 and approved >= quote["cap_credits"]:
        result = project.reconcile(saved["revision"], quote)
        print("Concluída:", project.wait(result["run"]["id"])["id"])
    else:
        print("Para aprovar, defina DOCMATCH_APPROVE_CAP com o limite aceito e execute novamente.")
else:
    print("Resultado indisponível; confira o projeto no app.")
