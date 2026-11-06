// This example approves the quote. Run only when you accept its cap.
use docmatch::Client;
use serde_json::json;
use std::{env, fs, path::Path, time::Duration};
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let c = Client::new(
        &env::var("DM_API_BASE_URL")?,
        &env::var("DOCMATCH_API_KEY")?,
    )?;
    let mut ids = vec![];
    for name in env::args().skip(1) {
        let p = Path::new(&name);
        let mime = if p.extension().and_then(|s| s.to_str()) == Some("txt") {
            "text/plain"
        } else {
            "text/csv"
        };
        let u = c
            .upload(p.file_name().unwrap().to_str().unwrap(), fs::read(p)?, mime)
            .await?;
        ids.push(u.id)
    }
    let input = c.input(&ids);
    let quote = c.estimate(&input).await?;
    eprintln!("Approved example run: cap {} tokens", quote.cap_credits);
    let r = c.run(&quote, &input).await?;
    c.wait(&r.id, Duration::from_secs(600)).await?;
    let run = c
        .request(reqwest::Method::GET, &format!("/v1/runs/{}", r.id), None)
        .await?;
    let findings = c
        .request(
            reqwest::Method::GET,
            &format!("/v1/runs/{}/findings?limit=100", r.id),
            None,
        )
        .await?;
    let typed = c.findings(&r.id).await?;
    assert_eq!(typed.len(), findings["items"].as_array().unwrap().len());
    let data = c.export(&r.id, "xlsx").await?;
    if let Ok(path) = env::var("DOCMATCH_EXPORT_PATH") {
        fs::write(path, data)?
    }
    println!("{}", json!({"run":run,"findings":findings["items"]}));
    Ok(())
}
