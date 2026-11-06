// Apache-2.0. Project handles keep uploads and results in one destination.
import type { Client, Finding, Quote, Run, Upload } from './index.js';
import { operations } from './operations.js';
export interface Page<T> { items: T[]; next_cursor?: string | null }
export interface Capabilities { manage_account: boolean; create_project: boolean; reconcile: boolean; read_results: boolean; upload: boolean }
export interface ProjectSettings { rules_text?: string; use_rules: boolean; web_search?: boolean; rule_ids?: string[]; llm_mode?: string }
export interface ProjectDocument {
 id: string; upload_id: string; file_name: string; role: string; status: string; size_bytes: number;
 replaces_id?: string; pending_replacement_id?: string; created_at: string; expires_at: string | null; removed_at: string | null;
}
export interface Project {
 id: string; name: string; visibility: 'private' | 'team'; version: number; revision: number;
 document_count: number; settings?: ProjectSettings; capabilities: Capabilities;
 created_at: string; updated_at: string; archived_at: string | null; documents?: ProjectDocument[]; shared_with?: string[];
}
export interface ProjectPatch { name?: string; archived?: boolean; settings?: ProjectSettings }
export interface ProjectAnalysis {
 state: 'ready' | 'unchanged' | 'queued' | 'running' | 'needs_approval' | 'expired' | 'failed' | 'cancelled';
 revision: number; quote?: Quote; run?: Run;
}
export interface ProjectRule { id: string; title: string; body: string; version: number; status: 'draft' | 'candidate' | 'verified' | 'trusted' | 'retired'; provenance: { origin: string } }
export interface ProjectEvent { id: number; action: string; created_at: string }
export class ProjectClient {
 constructor(private client: Client, readonly id: string, private lane: string) {}
 private path(operation: keyof typeof operations, ids: Record<string,string> = {}) {
  let path: string = operations[operation].path.replace('{project}', encodeURIComponent(this.id));
  for (const [key,value] of Object.entries(ids)) path = path.replace(`{${key}}`,encodeURIComponent(value));
  return path;
 }
 get(signal?: AbortSignal): Promise<Project> {return this.client.request('GET',this.path('getProject'),undefined,signal);}
 update(version: number, patch: ProjectPatch, signal?: AbortSignal): Promise<Project> {return this.client.request('PATCH',this.path('updateProject'),{...patch,version},signal);}
 delete(version: number, signal?: AbortSignal): Promise<void> {return this.client.request('DELETE',this.path('deleteProject')+`?version=${version}`,undefined,signal);}
 share(version: number, visibility: Project['visibility'], userIds: string[] = [], signal?: AbortSignal): Promise<Project> {return this.client.request('PUT',this.path('shareProject'),{version,visibility,user_ids:userIds},signal);}
 upload(name: string,data: Uint8Array,mime = 'application/octet-stream',options: {role?: string; replacesId?: string; signal?: AbortSignal} = {}): Promise<Upload> {return this.client.upload(name,data,mime,options.signal,{projectId:this.id,role:options.role,replacesId:options.replacesId});}
 attachDocument(uploadId: string,role = '',signal?: AbortSignal): Promise<{id: string}> {return this.client.request('POST',this.path('attachProjectDocument'),{upload_id:uploadId,role},signal);}
 removeDocument(id: string,version: number,signal?: AbortSignal): Promise<void> {return this.client.request('DELETE',this.path('removeProjectDocument',{document:id})+`?version=${version}`,undefined,signal);}
 replaceDocument(previousId: string,replacementId: string,version: number,signal?: AbortSignal): Promise<Project> {return this.client.request('POST',this.path('replaceProjectDocument',{document:previousId}),{version,replacement_id:replacementId},signal);}
 estimate(revision: number,reprocessReason = '',signal?: AbortSignal): Promise<ProjectAnalysis> {return this.client.request('POST',this.path('estimateProject'),{revision,lane:this.lane,reprocess_reason:reprocessReason},signal);}
 /** Pass a quote only after approving its cap. Existing valid results are reused. */
 reconcile(revision: number,approvedQuote?: Quote,reprocessReason = '',signal?: AbortSignal): Promise<ProjectAnalysis> {return this.client.request('POST',this.path('reconcileProject'),{revision,lane:this.lane,reprocess_reason:reprocessReason,...(approvedQuote?{quote_id:approvedQuote.id,quote_signature:approvedQuote.signature}:{})},signal);}
 runs(cursor = '',signal?: AbortSignal): Promise<Page<Run>> {return this.client.request('GET',this.path('listProjectRuns')+'?cursor='+encodeURIComponent(cursor),undefined,signal);}
 getRun(id: string,signal?: AbortSignal): Promise<Run> {return this.client.request('GET',this.path('projectGetRun',{id}),undefined,signal);}
 wait(id: string,options: {signal?: AbortSignal; timeoutMs?: number; pollMs?: number; onProgress?: (run: Run) => void} = {}): Promise<Run> {return this.client.wait(id,{...options,projectId:this.id});}
 findings(id: string,signal?: AbortSignal): Promise<Finding[]> {return this.client.findings(id,signal,this.id);}
 export(id: string,format: 'xlsx' | 'csv' | 'pdf',signal?: AbortSignal): Promise<Uint8Array> {return this.client.export(id,format,signal,this.id);}
 cancel(id: string,signal?: AbortSignal): Promise<Run> {return this.client.request('POST',this.path('projectCancelRun',{id}),{},signal);}
 approveQuote(id: string,approvedQuote: Quote,signal?: AbortSignal): Promise<Run> {return this.client.request('POST',this.path('projectApproveRunQuote',{id}),{quote_id:approvedQuote.id,quote_signature:approvedQuote.signature},signal);}
 rules(cursor = '',signal?: AbortSignal): Promise<Page<ProjectRule>> {return this.client.request('GET',this.path('projectListRules')+'?cursor='+encodeURIComponent(cursor),undefined,signal);}
 createRule(title: string,body: string,signal?: AbortSignal): Promise<ProjectRule> {return this.client.request('POST',this.path('projectCreateRule'),{title,body},signal);}
 versionRule(id: string,title: string,body: string,signal?: AbortSignal): Promise<ProjectRule> {return this.client.request('POST',this.path('projectCreateRuleVersion',{id}),{title,body},signal);}
 promoteRule(id: string,to: 'verified' | 'trusted' | 'retired',signal?: AbortSignal): Promise<ProjectRule> {return this.client.request('POST',this.path('projectPromoteRule',{id}),{to},signal);}
 activity(signal?: AbortSignal): Promise<Page<ProjectEvent>> {return this.client.request('GET',this.path('projectActivity'),undefined,signal);}
}
