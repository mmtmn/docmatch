// Apache-2.0. Inspect a saved project; approval is explicit and limited.
use docmatch::Client;
use std::{env, time::Duration};
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = Client::new(
        &env::var("DM_API_BASE_URL")?,
        &env::var("DOCMATCH_API_KEY")?,
    )?;
    let id = env::args()
        .nth(1)
        .ok_or("uso: cargo run --example project -- prj_ID")?;
    let project = client.project(&id)?;
    let saved = project.get().await?;
    let analysis = project.estimate(saved.revision, "").await?;
    if let Some(run) = analysis.run {
        println!("Conciliação existente: {} {}", run.id, run.status);
    } else if let Some(quote) = analysis.quote {
        println!("{} · limite: {} créditos", saved.name, quote.cap_credits);
        let approved = env::var("DOCMATCH_APPROVE_CAP")
            .ok()
            .and_then(|s| s.parse::<i64>().ok())
            .unwrap_or(0);
        if approved > 0 && approved >= quote.cap_credits {
            let result = project.reconcile(saved.revision, Some(&quote), "").await?;
            let run = result.run.ok_or("Resposta sem conciliação")?;
            println!(
                "Concluída: {}",
                project.wait(&run.id, Duration::from_secs(600)).await?.id
            );
        } else {
            println!("Para aprovar, defina DOCMATCH_APPROVE_CAP com o limite aceito e execute novamente.");
        }
    } else {
        println!("Resultado indisponível; confira o projeto no app.");
    }
    Ok(())
}
