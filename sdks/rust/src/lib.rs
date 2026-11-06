//! Official docmatch API client. Apache-2.0. No telemetry.
mod operations;
mod projects;
use hmac::{Hmac, Mac};
pub use operations::OPERATIONS;
pub use projects::*;
use reqwest::{Client as Http, Method, Url};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::{
    fmt,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

#[derive(Debug, Deserialize)]
pub struct Error {
    #[serde(default)]
    pub status: u16,
    #[serde(default)]
    pub code: String,
    #[serde(default)]
    pub message: String,
    #[serde(default)]
    pub request_id: Option<String>,
    #[serde(default)]
    pub details: Value,
}
impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{} ({} {})", self.message, self.status, self.code)
    }
}
impl std::error::Error for Error {}
fn failure(code: &str, message: &str) -> Error {
    Error {
        status: 0,
        code: code.into(),
        message: message.into(),
        request_id: None,
        details: Value::Null,
    }
}
impl From<reqwest::Error> for Error {
    fn from(_: reqwest::Error) -> Self {
        failure("network_error", "Falha na conexão com a API.")
    }
}
impl From<serde_json::Error> for Error {
    fn from(_: serde_json::Error) -> Self {
        failure("invalid_response", "Resposta inválida da API.")
    }
}
#[derive(Clone)]
pub struct Client {
    base_url: String,
    key: String,
    pub lane: String,
    http: Http,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Quote {
    pub id: String,
    pub signature: String,
    pub expected_credits: i64,
    pub cap_credits: i64,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Run {
    #[serde(default)]
    pub project_id: Option<String>,
    pub id: String,
    pub status: String,
    #[serde(default)]
    pub summary: Value,
    #[serde(default)]
    pub error: Value,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Upload {
    pub id: String,
    pub status: String,
    #[serde(default)]
    pub file_name: String,
    #[serde(default)]
    pub rejected_reason: Option<String>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Finding {
    pub id: String,
    pub code: String,
    pub title: String,
    pub severity: String,
    pub amount_at_risk: i64,
    #[serde(default)]
    pub evidence: Vec<Value>,
}
fn safe_url(value: &str) -> Result<Url, Error> {
    let u = Url::parse(value).map_err(|_| failure("invalid_url", "URL inválida."))?;
    let local = matches!(u.host_str(), Some("localhost" | "127.0.0.1" | "[::1]"));
    if !u.username().is_empty()
        || u.password().is_some()
        || !(u.scheme() == "https" || (u.scheme() == "http" && local))
    {
        return Err(failure(
            "invalid_url",
            "HTTPS obrigatório fora do localhost.",
        ));
    }
    Ok(u)
}
fn route(operation: &str, id: &str) -> Result<String, Error> {
    if !id.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') {
        return Err(failure("invalid_id", "Identificador inválido."));
    }
    let path = OPERATIONS
        .iter()
        .find(|(name, _, _)| *name == operation)
        .ok_or_else(|| failure("invalid_operation", "Operação inválida."))?
        .2;
    Ok(path.replace("{id}", id))
}
fn hex(data: &[u8]) -> String {
    data.iter().map(|b| format!("{b:02x}")).collect()
}
fn delay(header: Option<&str>, attempt: u32) -> Duration {
    if let Some(h) = header {
        if let Ok(n) = h.parse::<u64>() {
            return Duration::from_secs(n);
        }
        if let Ok(t) = httpdate::parse_http_date(h) {
            return t.duration_since(SystemTime::now()).unwrap_or_default();
        }
    }
    Duration::from_millis(250 * (1 << attempt) + u64::from(uuid::Uuid::new_v4().as_bytes()[0]))
}
impl Client {
    pub fn new(base_url: &str, key: &str) -> Result<Self, Error> {
        let u = safe_url(base_url)?;
        if u.query().is_some() || u.fragment().is_some() {
            return Err(failure("invalid_url", "Use a origem da API."));
        }
        Ok(Self {
            base_url: base_url.trim_end_matches('/').into(),
            key: key.into(),
            lane: "api_batch".into(),
            http: Http::builder()
                .timeout(Duration::from_secs(60))
                .redirect(reqwest::redirect::Policy::none())
                .build()?,
        })
    }
    pub fn input(&self, ids: &[String]) -> Value {
        json!({"sources":ids.iter().map(|id|json!({"upload_id":id})).collect::<Vec<_>>(),"options":{"lane":self.lane,"use_rules":true,"web_search":false}})
    }
    async fn response(
        &self,
        method: Method,
        path: &str,
        body: Option<&Value>,
    ) -> Result<reqwest::Response, Error> {
        if !path.starts_with("/v1/") || path.contains('\\') {
            return Err(failure("invalid_path", "Use um caminho relativo /v1/."));
        }
        let key = uuid::Uuid::new_v4().to_string();
        let retry = method == Method::GET
            || (method == Method::POST
                && (path == "/v1/runs"
                    || (path.starts_with("/v1/projects/") && path.ends_with("/runs"))));
        for attempt in 0..4 {
            let mut req = self
                .http
                .request(method.clone(), format!("{}{}", self.base_url, path))
                .bearer_auth(&self.key)
                .header("Accept", "application/json");
            if method != Method::GET {
                req = req.header("Idempotency-Key", &key)
            };
            if let Some(b) = body {
                req = req.json(b)
            }
            let res = match req.send().await {
                Ok(r) => r,
                Err(e) => {
                    if retry && attempt < 3 {
                        tokio::time::sleep(delay(None, attempt)).await;
                        continue;
                    }
                    return Err(e.into());
                }
            };
            if res.status().is_success() {
                return Ok(res);
            }
            if attempt < 3
                && (res.status().as_u16() == 429 || (retry && res.status().is_server_error()))
            {
                tokio::time::sleep(delay(
                    res.headers()
                        .get("Retry-After")
                        .and_then(|h| h.to_str().ok()),
                    attempt,
                ))
                .await;
                continue;
            }
            let status = res.status().as_u16();
            let mut error = res
                .json::<Error>()
                .await
                .unwrap_or_else(|_| failure("http_error", "Falha na API."));
            error.status = status;
            return Err(error);
        }
        Err(failure("retry_exhausted", "Tentativas esgotadas."))
    }
    pub async fn request(
        &self,
        method: Method,
        path: &str,
        body: Option<&Value>,
    ) -> Result<Value, Error> {
        let res = self.response(method, path, body).await?;
        if res.status().as_u16() == 204 {
            return Ok(Value::Null);
        }
        Ok(res.json().await?)
    }
    pub async fn estimate(&self, input: &Value) -> Result<Quote, Error> {
        Ok(serde_json::from_value(
            self.request(Method::POST, &route("estimateRun", "")?, Some(input))
                .await?,
        )?)
    }
    pub async fn run(&self, quote: &Quote, input: &Value) -> Result<Run, Error> {
        let mut body = input.clone();
        let map = body
            .as_object_mut()
            .ok_or_else(|| failure("invalid_input", "Entrada inválida."))?;
        map.insert("quote_id".into(), json!(quote.id));
        map.insert("quote_signature".into(), json!(quote.signature));
        Ok(serde_json::from_value(
            self.request(Method::POST, &route("createRun", "")?, Some(&body))
                .await?,
        )?)
    }
    pub async fn get_run(&self, id: &str) -> Result<Run, Error> {
        Ok(serde_json::from_value(
            self.request(Method::GET, &route("getRun", id)?, None)
                .await?,
        )?)
    }
    pub async fn wait(&self, id: &str, timeout: Duration) -> Result<Run, Error> {
        tokio::time::timeout(timeout, async {
            loop {
                let r = self.get_run(id).await?;
                match r.status.as_str() {
                    "completed" => return Ok(r),
                    "failed" | "cancelled" | "needs_approval" => {
                        return Err(failure(
                            &r.status,
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
        self.findings_at(&route("listFindings", id)?).await
    }
    async fn findings_at(&self, prefix: &str) -> Result<Vec<Finding>, Error> {
        let mut all = vec![];
        let mut cursor = String::new();
        for _ in 0..1000 {
            let mut u = Url::parse(&format!("{}{}", self.base_url, prefix))
                .map_err(|_| failure("invalid_url", "URL inválida."))?;
            u.query_pairs_mut()
                .append_pair("limit", "100")
                .append_pair("cursor", &cursor);
            let path = format!("{}?{}", u.path(), u.query().unwrap_or(""));
            let data = self.request(Method::GET, &path, None).await?;
            all.extend(serde_json::from_value::<Vec<Finding>>(
                data["items"].clone(),
            )?);
            let next = data["next_cursor"].as_str().unwrap_or("");
            if next.is_empty() {
                return Ok(all);
            }
            if next == cursor {
                return Err(failure("pagination", "Cursor repetido."));
            }
            cursor = next.into()
        }
        Err(failure("pagination", "Limite de páginas atingido."))
    }
    pub async fn export(&self, id: &str, format: &str) -> Result<Vec<u8>, Error> {
        if !matches!(format, "xlsx" | "csv" | "pdf") {
            return Err(failure("invalid_format", "Formato inválido."));
        }
        Ok(self
            .response(
                Method::GET,
                &format!("{}?format={}", route("exportRun", id)?, format),
                None,
            )
            .await?
            .bytes()
            .await?
            .to_vec())
    }
    pub async fn upload(&self, name: &str, data: Vec<u8>, mime: &str) -> Result<Upload, Error> {
        self.upload_at(&route("createUpload", "")?, name, data, mime, json!({}))
            .await
    }
    async fn upload_at(
        &self,
        prefix: &str,
        name: &str,
        data: Vec<u8>,
        mime: &str,
        extra: Value,
    ) -> Result<Upload, Error> {
        let digest = hex(&Sha256::digest(&data));
        let mut body = json!({"kind":"document","file_name":name,"mime":mime,"size_bytes":data.len(),"sha256":digest});
        if let (Some(body), Some(extra)) = (body.as_object_mut(), extra.as_object()) {
            body.extend(extra.clone());
        }
        let ticket = self.request(Method::POST, prefix, Some(&body)).await?;
        let url = safe_url(
            ticket["url"]
                .as_str()
                .ok_or_else(|| failure("invalid_ticket", "Ticket inválido."))?,
        )?;
        // A new client has neither API authentication nor cookies.
        let storage = Http::builder()
            .timeout(Duration::from_secs(120))
            .redirect(reqwest::redirect::Policy::none())
            .build()?;
        let req = match ticket["method"].as_str() {
            Some("POST") => {
                let mut form = reqwest::multipart::Form::new();
                if let Some(fields) = ticket["fields"].as_object() {
                    for (k, v) in fields {
                        form = form.text(k.clone(), v.as_str().unwrap_or("").to_string())
                    }
                }
                storage.post(url).multipart(
                    form.part(
                        "file",
                        reqwest::multipart::Part::bytes(data)
                            .file_name(name.to_owned())
                            .mime_str(mime)?,
                    ),
                )
            }
            Some("PUT") => {
                let mut req = storage.put(url).body(data);
                if let Some(headers) = ticket["headers"].as_object() {
                    for (k, v) in headers {
                        req = req.header(k, v.as_str().unwrap_or(""))
                    }
                }
                req
            }
            _ => return Err(failure("invalid_ticket", "Método inválido.")),
        };
        let res = req.send().await?;
        if !res.status().is_success() {
            return Err(failure("upload_failed", "Falha no envio do arquivo."));
        }
        let id = ticket["id"]
            .as_str()
            .ok_or_else(|| failure("invalid_ticket", "Ticket inválido."))?;
        route("completeUpload", id)?; // validate the server-returned identifier
        let path = format!("{prefix}/{id}/complete");
        for _ in 0..120 {
            let upload: Upload = serde_json::from_value(
                self.request(Method::POST, &path, Some(&json!({"sha256":digest})))
                    .await?,
            )?;
            if upload.status == "ready" {
                return Ok(upload);
            }
            if !matches!(upload.status.as_str(), "pending" | "scanning") {
                return Err(failure("upload_rejected", "Arquivo recusado."));
            }
            tokio::time::sleep(Duration::from_secs(1)).await
        }
        Err(failure("timeout", "Tempo de verificação esgotado."))
    }
}
pub fn verify_webhook(body: &[u8], header: &str, secret: &str, now: u64) -> bool {
    if secret.is_empty() {
        return false;
    }
    let parts: Vec<_> = header
        .split(',')
        .filter_map(|p| p.trim().split_once('='))
        .collect();
    let timestamps: Vec<_> = parts.iter().filter(|(k, _)| *k == "t").collect();
    if timestamps.len() != 1 {
        return false;
    }
    let text = timestamps[0].1;
    let timestamp = match text.parse::<u64>() {
        Ok(t) => t,
        Err(_) => return false,
    };
    if now.abs_diff(timestamp) > 300 {
        return false;
    }
    let mut mac =
        Hmac::<Sha256>::new_from_slice(secret.as_bytes()).expect("HMAC supports all key lengths");
    mac.update(format!("{text}.").as_bytes());
    mac.update(body);
    for (k, v) in parts {
        if k == "v1" && v.len() == 64 && v.is_ascii() {
            let bytes: Result<Vec<u8>, _> = (0..64)
                .step_by(2)
                .map(|i| u8::from_str_radix(&v[i..i + 2], 16))
                .collect();
            if let Ok(sig) = bytes {
                if mac.clone().verify_slice(&sig).is_ok() {
                    return true;
                }
            }
        }
    }
    false
}
pub fn unix_time() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}
