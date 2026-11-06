//! Saved projects. Apache-2.0. The handle fixes the destination of multi-step operations.
use super::*;
use serde::de::DeserializeOwned;

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Page<T> {
    pub items: Vec<T>,
    pub next_cursor: Option<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Capabilities {
    pub manage_account: bool,
    pub create_project: bool,
    pub reconcile: bool,
    pub read_results: bool,
    pub upload: bool,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Project {
    pub id: String,
    pub name: String,
    pub visibility: String,
    pub version: i64,
    pub revision: i64,
    pub document_count: u64,
    pub capabilities: Capabilities,
    pub created_at: String,
    pub updated_at: String,
    pub archived_at: Option<String>,
    #[serde(default)]
    pub settings: Value,
    #[serde(default)]
    pub documents: Vec<ProjectDocument>,
    #[serde(default)]
    pub shared_with: Vec<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ProjectDocument {
    pub id: String,
    pub upload_id: String,
    pub file_name: String,
    pub role: String,
    pub status: String,
    pub size_bytes: u64,
    pub created_at: String,
    pub expires_at: Option<String>,
    pub removed_at: Option<String>,
    pub replaces_id: Option<String>,
    pub pending_replacement_id: Option<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ProjectAnalysis {
    pub state: String,
    pub revision: i64,
    pub quote: Option<Quote>,
    pub run: Option<Run>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ProjectRule {
    pub id: String,
    pub title: String,
    pub body: String,
    pub version: i64,
    pub status: String,
    pub provenance: Value,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ProjectEvent {
    pub id: i64,
    pub action: String,
    pub created_at: String,
}
#[derive(Clone)]
pub struct ProjectClient {
    client: Client,
    pub id: String,
}
fn cursor_path(path: &str, cursor: &str) -> String {
    let query: String = url_query(cursor);
    format!("{path}?cursor={query}")
}
fn url_query(cursor: &str) -> String {
    // Use URL's encoder so opaque cursors cannot inject another query parameter.
    let mut url = Url::parse("https://docmatch.invalid").expect("constant URL");
    url.query_pairs_mut().append_pair("cursor", cursor);
    url.query()
        .unwrap_or("cursor=")
        .trim_start_matches("cursor=")
        .to_owned()
}
impl Client {
    pub fn project(&self, id: &str) -> Result<ProjectClient, Error> {
        if id.is_empty() || !id.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') {
            return Err(failure("invalid_id", "Identificador inválido."));
        }
        Ok(ProjectClient {
            client: self.clone(),
            id: id.into(),
        })
    }
    pub async fn list_projects(
        &self,
        archived: bool,
        cursor: &str,
    ) -> Result<Page<Project>, Error> {
        Ok(serde_json::from_value(
            self.request(
                Method::GET,
                &format!(
                    "{}&archived={archived}",
                    cursor_path(&route("listProjects", "")?, cursor)
                ),
                None,
            )
            .await?,
        )?)
    }
    pub async fn create_project(&self, name: &str) -> Result<Project, Error> {
        Ok(serde_json::from_value(
            self.request(
                Method::POST,
                &route("createProject", "")?,
                Some(&json!({"name":name})),
            )
            .await?,
        )?)
    }
}
impl ProjectClient {
    fn path(&self, operation: &str, id: &str) -> Result<String, Error> {
        Ok(route(operation, id)?
            .replace("{project}", &self.id)
            .replace("{document}", id))
    }
    async fn json<T: DeserializeOwned>(
        &self,
        method: Method,
        path: &str,
        body: Option<&Value>,
    ) -> Result<T, Error> {
        Ok(serde_json::from_value(
            self.client.request(method, path, body).await?,
        )?)
    }
    pub async fn get(&self) -> Result<Project, Error> {
        self.json(Method::GET, &self.path("getProject", "")?, None)
            .await
    }
    pub async fn update(&self, version: i64, patch: &Value) -> Result<Project, Error> {
        let mut body = patch
            .as_object()
            .cloned()
            .ok_or_else(|| failure("invalid_patch", "As alterações devem ser um objeto."))?;
        body.insert("version".into(), json!(version));
        self.json(
            Method::PATCH,
            &self.path("updateProject", "")?,
            Some(&Value::Object(body)),
        )
        .await
    }
    pub async fn delete(&self, version: i64) -> Result<(), Error> {
        self.client
            .request(
                Method::DELETE,
                &format!("{}?version={version}", self.path("deleteProject", "")?),
                None,
            )
            .await?;
        Ok(())
    }
    pub async fn share(
        &self,
        version: i64,
        visibility: &str,
        user_ids: &[String],
    ) -> Result<Project, Error> {
        self.json(
            Method::PUT,
            &self.path("shareProject", "")?,
            Some(&json!({"version":version,"visibility":visibility,"user_ids":user_ids})),
        )
        .await
    }
    pub async fn upload(
        &self,
        name: &str,
        data: Vec<u8>,
        mime: &str,
        role: &str,
    ) -> Result<Upload, Error> {
        self.client
            .upload_at(
                &self.path("createProjectUpload", "")?,
                name,
                data,
                mime,
                json!({"role":role}),
            )
            .await
    }
    pub async fn upload_replacement(
        &self,
        document: &str,
        name: &str,
        data: Vec<u8>,
        mime: &str,
    ) -> Result<Upload, Error> {
        self.client
            .upload_at(
                &self.path("createProjectUpload", "")?,
                name,
                data,
                mime,
                json!({"replaces_id":document}),
            )
            .await
    }
    pub async fn attach_document(&self, upload_id: &str, role: &str) -> Result<String, Error> {
        let value: Value = self
            .json(
                Method::POST,
                &self.path("attachProjectDocument", "")?,
                Some(&json!({"upload_id":upload_id,"role":role})),
            )
            .await?;
        value["id"]
            .as_str()
            .map(str::to_owned)
            .ok_or_else(|| failure("invalid_response", "Documento sem identificador."))
    }
    pub async fn remove_document(&self, document: &str, version: i64) -> Result<(), Error> {
        self.client
            .request(
                Method::DELETE,
                &format!(
                    "{}?version={version}",
                    self.path("removeProjectDocument", document)?
                ),
                None,
            )
            .await?;
        Ok(())
    }
    pub async fn replace_document(
        &self,
        previous: &str,
        replacement: &str,
        version: i64,
    ) -> Result<Project, Error> {
        self.json(
            Method::POST,
            &self.path("replaceProjectDocument", previous)?,
            Some(&json!({"version":version,"replacement_id":replacement})),
        )
        .await
    }
    pub async fn estimate(
        &self,
        revision: i64,
        reprocess_reason: &str,
    ) -> Result<ProjectAnalysis, Error> {
        self.json(Method::POST,&self.path("estimateProject", "")?,Some(&json!({"revision":revision,"lane":self.client.lane,"reprocess_reason":reprocess_reason}))).await
    }
    /// Pass a quote only after approving its cap. Existing results do not reserve credits again.
    pub async fn reconcile(
        &self,
        revision: i64,
        approved_quote: Option<&Quote>,
        reprocess_reason: &str,
    ) -> Result<ProjectAnalysis, Error> {
        let mut body = json!({"revision":revision,"lane":self.client.lane,"reprocess_reason":reprocess_reason});
        if let Some(q) = approved_quote {
            body["quote_id"] = json!(q.id);
            body["quote_signature"] = json!(q.signature);
        }
        self.json(
            Method::POST,
            &self.path("reconcileProject", "")?,
            Some(&body),
        )
        .await
    }
    pub async fn runs(&self, cursor: &str) -> Result<Page<Run>, Error> {
        self.json(
            Method::GET,
            &cursor_path(&self.path("listProjectRuns", "")?, cursor),
            None,
        )
        .await
    }
    pub async fn get_run(&self, id: &str) -> Result<Run, Error> {
        self.json(Method::GET, &self.path("projectGetRun", id)?, None)
            .await
    }
    pub async fn wait(&self, id: &str, timeout: Duration) -> Result<Run, Error> {
        tokio::time::timeout(timeout, async {
            loop {
                let run = self.get_run(id).await?;
                match run.status.as_str() {
                    "completed" => return Ok(run),
                    "failed" | "cancelled" | "needs_approval" => {
                        return Err(failure(
                            &run.status,
                            "Execução interrompida ou aguardando aprovação.",
                        ))
                    }
                    _ => tokio::time::sleep(Duration::from_secs(1)).await,
                }
            }
        })
        .await
        .map_err(|_| failure("timeout", "Tempo de espera esgotado."))?
    }
    pub async fn findings(&self, id: &str) -> Result<Vec<Finding>, Error> {
        self.client
            .findings_at(&self.path("projectListFindings", id)?)
            .await
    }
    pub async fn export(&self, id: &str, format: &str) -> Result<Vec<u8>, Error> {
        if !matches!(format, "xlsx" | "csv" | "pdf") {
            return Err(failure("invalid_format", "Formato inválido."));
        }
        Ok(self
            .client
            .response(
                Method::GET,
                &format!("{}?format={format}", self.path("projectExportRun", id)?),
                None,
            )
            .await?
            .bytes()
            .await?
            .to_vec())
    }
    pub async fn cancel(&self, id: &str) -> Result<Run, Error> {
        self.json(
            Method::POST,
            &self.path("projectCancelRun", id)?,
            Some(&json!({})),
        )
        .await
    }
    pub async fn approve_quote(&self, id: &str, quote: &Quote) -> Result<Run, Error> {
        self.json(
            Method::POST,
            &self.path("projectApproveRunQuote", id)?,
            Some(&json!({"quote_id":quote.id,"quote_signature":quote.signature})),
        )
        .await
    }
    pub async fn rules(&self, cursor: &str) -> Result<Page<ProjectRule>, Error> {
        self.json(
            Method::GET,
            &cursor_path(&self.path("projectListRules", "")?, cursor),
            None,
        )
        .await
    }
    pub async fn create_rule(&self, title: &str, body: &str) -> Result<ProjectRule, Error> {
        self.json(
            Method::POST,
            &self.path("projectCreateRule", "")?,
            Some(&json!({"title":title,"body":body})),
        )
        .await
    }
    pub async fn version_rule(
        &self,
        id: &str,
        title: &str,
        body: &str,
    ) -> Result<ProjectRule, Error> {
        self.json(
            Method::POST,
            &self.path("projectCreateRuleVersion", id)?,
            Some(&json!({"title":title,"body":body})),
        )
        .await
    }
    pub async fn promote_rule(&self, id: &str, to: &str) -> Result<ProjectRule, Error> {
        self.json(
            Method::POST,
            &self.path("projectPromoteRule", id)?,
            Some(&json!({"to":to})),
        )
        .await
    }
    pub async fn activity(&self) -> Result<Page<ProjectEvent>, Error> {
        self.json(Method::GET, &self.path("projectActivity", "")?, None)
            .await
    }
}
