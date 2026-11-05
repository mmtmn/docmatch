//! Compiles `packages/contracts/harness.proto` with a vendored `protoc`, so building the
//! harness never needs a system install.

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let manifest = std::path::PathBuf::from(std::env::var("CARGO_MANIFEST_DIR")?);
    let contracts = manifest.join("../../../../packages/contracts");
    let proto = contracts.join("harness.proto");
    println!("cargo:rerun-if-changed={}", proto.display());
    let mut config = prost_build::Config::new();
    config.protoc_executable(protoc_bin_vendored::protoc_bin_path()?);
    let well_known = protoc_bin_vendored::include_path()?;
    tonic_prost_build::configure()
        .build_server(true)
        .build_client(true)
        .compile_with_config(config, &[proto], &[contracts, well_known])?;
    Ok(())
}
