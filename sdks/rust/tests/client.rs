use docmatch::{verify_webhook, Client};
use hmac::{Hmac, Mac};
use sha2::Sha256;
#[test]
fn rejects_insecure_remote() {
    assert!(Client::new("http://remote.invalid", "key").is_err());
    assert!(Client::new("http://127.0.0.1:8080", "key").is_ok())
}
#[test]
fn verifies_webhook() {
    let body = b"{}";
    let mut h = Hmac::<Sha256>::new_from_slice(b"secret").unwrap();
    h.update(b"1234567890.");
    h.update(body);
    let sig: String = h
        .finalize()
        .into_bytes()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let header = format!("t=1234567890,v1={sig}");
    assert!(verify_webhook(body, &header, "secret", 1234567890));
    assert!(!verify_webhook(b"changed", &header, "secret", 1234567890));
    assert!(!verify_webhook(body, &header, "secret", 1234568191));
    assert!(!verify_webhook(
        body,
        &format!("{header},t=1234567890"),
        "secret",
        1234567890
    ));
}

#[tokio::test]
async fn project_retry_preserves_key_and_returns_saved_result() {
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::time::Duration;
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = std::thread::spawn(move || {
        let mut keys = Vec::new();
        for attempt in 0..2 {
            let (mut socket, _) = listener.accept().unwrap();
            socket
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut bytes = Vec::new();
            let mut chunk = [0u8; 1024];
            let headers_end = loop {
                let count = socket.read(&mut chunk).unwrap();
                assert!(count > 0);
                bytes.extend_from_slice(&chunk[..count]);
                if let Some(end) = bytes.windows(4).position(|w| w == b"\r\n\r\n") {
                    break end + 4;
                }
            };
            let headers = String::from_utf8(bytes[..headers_end].to_vec()).unwrap();
            assert!(headers.starts_with("POST /v1/projects/prj_one/runs HTTP/1.1"));
            let mut size = 0;
            for line in headers.lines() {
                if let Some((name, value)) = line.split_once(':') {
                    if name.eq_ignore_ascii_case("idempotency-key") {
                        keys.push(value.trim().to_owned());
                    }
                    if name.eq_ignore_ascii_case("content-length") {
                        size = value.trim().parse::<usize>().unwrap();
                    }
                }
            }
            while bytes.len() < headers_end + size {
                let count = socket.read(&mut chunk).unwrap();
                assert!(count > 0);
                bytes.extend_from_slice(&chunk[..count]);
            }
            let body: serde_json::Value =
                serde_json::from_slice(&bytes[headers_end..headers_end + size]).unwrap();
            assert_eq!(body["revision"], 4);
            assert!(body.get("quote_id").is_none());
            let (status, body) = if attempt == 0 {
                ("503 Service Unavailable", "{}")
            } else {
                (
                    "200 OK",
                    r#"{"state":"unchanged","revision":4,"run":{"id":"run_saved","status":"completed"}}"#,
                )
            };
            write!(socket,"HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nRetry-After: 0\r\nConnection: close\r\n\r\n{body}",body.len()).unwrap();
        }
        assert_eq!(keys.len(), 2);
        assert_eq!(keys[0], keys[1]);
    });
    let client = Client::new(&format!("http://{address}"), "fixture").unwrap();
    let project = client.project("prj_one").unwrap();
    let result = project.reconcile(4, None, "").await.unwrap();
    assert_eq!(result.state, "unchanged");
    assert_eq!(result.run.unwrap().id, "run_saved");
    assert!(result.quote.is_none());
    server.join().unwrap();
}
