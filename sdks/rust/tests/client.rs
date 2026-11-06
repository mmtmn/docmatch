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
