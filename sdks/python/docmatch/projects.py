"""Apache-2.0. A project handle fixes the destination of every multi-step operation."""
from urllib.parse import quote
from .operations import OPERATIONS

class Project:
    def __init__(self, client, project_id: str):
        self.client, self.id = client, project_id

    def _path(self, operation, **ids):
        path = OPERATIONS[operation]["path"].replace("{project}", quote(self.id, safe=""))
        for key, value in ids.items():
            path = path.replace("{" + key + "}", quote(value, safe=""))
        return path

    def get(self):
        return self.client.request("GET", self._path("getProject"))

    def update(self, version: int, **patch):
        return self.client.request("PATCH", self._path("updateProject"), {**patch, "version": version})

    def delete(self, version: int):
        return self.client.request("DELETE", self._path("deleteProject") + "?version=" + str(version))

    def share(self, version: int, visibility="private", user_ids=()):
        return self.client.request("PUT", self._path("shareProject"), {"version": version, "visibility": visibility, "user_ids": list(user_ids)})

    def upload(self, name: str, data: bytes, mime="application/octet-stream", *, role="", replaces_id=None):
        return self.client.upload(name, data, mime, project_id=self.id, role=role, replaces_id=replaces_id)

    def attach_document(self, upload_id: str, role=""):
        return self.client.request("POST", self._path("attachProjectDocument"), {"upload_id": upload_id, "role": role})

    def remove_document(self, document_id: str, version: int):
        return self.client.request("DELETE", self._path("removeProjectDocument", document=document_id) + "?version=" + str(version))

    def replace_document(self, previous_id: str, replacement_id: str, version: int):
        return self.client.request("POST", self._path("replaceProjectDocument", document=previous_id), {"version": version, "replacement_id": replacement_id})

    def estimate(self, revision: int, *, reprocess_reason=""):
        return self.client.request("POST", self._path("estimateProject"), {"revision": revision, "lane": self.client.lane, "reprocess_reason": reprocess_reason})

    def reconcile(self, revision: int, approved_quote=None, *, reprocess_reason=""):
        body = {"revision": revision, "lane": self.client.lane, "reprocess_reason": reprocess_reason}
        if approved_quote is not None:
            body.update(quote_id=approved_quote["id"], quote_signature=approved_quote["signature"])
        return self.client.request("POST", self._path("reconcileProject"), body)

    def runs(self, cursor=""):
        return self.client.request("GET", self._path("listProjectRuns") + "?cursor=" + quote(cursor, safe=""))

    def get_run(self, run_id: str):
        return self.client.get_run(run_id, project_id=self.id)

    def wait(self, run_id: str, **options):
        return self.client.wait(run_id, project_id=self.id, **options)

    def findings(self, run_id: str):
        return self.client.findings(run_id, project_id=self.id)

    def export(self, run_id: str, format="xlsx"):
        return self.client.export(run_id, format, project_id=self.id)

    def cancel(self, run_id: str):
        return self.client.request("POST", self._path("projectCancelRun", id=run_id), {})

    def approve_quote(self, run_id: str, approved_quote: dict):
        return self.client.request("POST", self._path("projectApproveRunQuote", id=run_id), {"quote_id": approved_quote["id"], "quote_signature": approved_quote["signature"]})

    def rules(self, cursor=""):
        return self.client.request("GET", self._path("projectListRules") + "?cursor=" + quote(cursor, safe=""))

    def create_rule(self, title: str, body: str):
        return self.client.request("POST", self._path("projectCreateRule"), {"title": title, "body": body})

    def version_rule(self, rule_id: str, title: str, body: str):
        return self.client.request("POST", self._path("projectCreateRuleVersion", id=rule_id), {"title": title, "body": body})

    def promote_rule(self, rule_id: str, to: str):
        return self.client.request("POST", self._path("projectPromoteRule", id=rule_id), {"to": to})

    def activity(self):
        return self.client.request("GET", self._path("projectActivity"))


class AsyncProject(Project):
    async def get(self):
        return await self.client.request("GET", self._path("getProject"))

    async def update(self, version: int, **patch):
        return await self.client.request("PATCH", self._path("updateProject"), {**patch, "version": version})

    async def delete(self, version: int):
        return await self.client.request("DELETE", self._path("deleteProject") + "?version=" + str(version))

    async def share(self, version: int, visibility="private", user_ids=()):
        return await self.client.request("PUT", self._path("shareProject"), {"version": version, "visibility": visibility, "user_ids": list(user_ids)})

    async def upload(self, name: str, data: bytes, mime="application/octet-stream", *, role="", replaces_id=None):
        return await self.client.upload(name, data, mime, project_id=self.id, role=role, replaces_id=replaces_id)

    async def attach_document(self, upload_id: str, role=""):
        return await self.client.request("POST", self._path("attachProjectDocument"), {"upload_id": upload_id, "role": role})

    async def remove_document(self, document_id: str, version: int):
        return await self.client.request("DELETE", self._path("removeProjectDocument", document=document_id) + "?version=" + str(version))

    async def replace_document(self, previous_id: str, replacement_id: str, version: int):
        return await self.client.request("POST", self._path("replaceProjectDocument", document=previous_id), {"version": version, "replacement_id": replacement_id})

    async def estimate(self, revision: int, *, reprocess_reason=""):
        return await self.client.request("POST", self._path("estimateProject"), {"revision": revision, "lane": self.client.lane, "reprocess_reason": reprocess_reason})

    async def reconcile(self, revision: int, approved_quote=None, *, reprocess_reason=""):
        body = {"revision": revision, "lane": self.client.lane, "reprocess_reason": reprocess_reason}
        if approved_quote is not None:
            body.update(quote_id=approved_quote["id"], quote_signature=approved_quote["signature"])
        return await self.client.request("POST", self._path("reconcileProject"), body)

    async def runs(self, cursor=""):
        return await self.client.request("GET", self._path("listProjectRuns") + "?cursor=" + quote(cursor, safe=""))

    async def get_run(self, run_id: str):
        return await self.client.get_run(run_id, project_id=self.id)

    async def wait(self, run_id: str, **options):
        return await self.client.wait(run_id, project_id=self.id, **options)

    async def findings(self, run_id: str):
        return await self.client.findings(run_id, project_id=self.id)

    async def export(self, run_id: str, format="xlsx"):
        return await self.client.export(run_id, format, project_id=self.id)

    async def cancel(self, run_id: str):
        return await self.client.request("POST", self._path("projectCancelRun", id=run_id), {})

    async def approve_quote(self, run_id: str, approved_quote: dict):
        return await self.client.request("POST", self._path("projectApproveRunQuote", id=run_id), {"quote_id": approved_quote["id"], "quote_signature": approved_quote["signature"]})

    async def rules(self, cursor=""):
        return await self.client.request("GET", self._path("projectListRules") + "?cursor=" + quote(cursor, safe=""))

    async def create_rule(self, title: str, body: str):
        return await self.client.request("POST", self._path("projectCreateRule"), {"title": title, "body": body})

    async def version_rule(self, rule_id: str, title: str, body: str):
        return await self.client.request("POST", self._path("projectCreateRuleVersion", id=rule_id), {"title": title, "body": body})

    async def promote_rule(self, rule_id: str, to: str):
        return await self.client.request("POST", self._path("projectPromoteRule", id=rule_id), {"to": to})

    async def activity(self):
        return await self.client.request("GET", self._path("projectActivity"))
