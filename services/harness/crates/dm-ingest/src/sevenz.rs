//! 7z archives (pure Rust, `sevenz-rust2` with LZMA and LZMA2 only): every entry re-enters the
//! dispatcher as its own source, under the same shared limits as a ZIP (`files.zip_*` of
//! `packages/contracts/limits.yaml`). The API's upload scanner has no 7z reader, so these
//! checks are the only ones: entry count, declared and actually decoded size, compression
//! ratio (whole archive and per block), an archive inside the archive, dangerous paths, and a
//! compressed header that would decode past [`MAX_HEADER`]. Entries are decoded one block at a
//! time on this thread, never past their declared size.

use std::io::{Cursor, Read};

use sevenz_rust2::{Archive, BlockDecoder, EncoderMethod, Password};

use crate::archive::{is_noise, safe_path, ZipEntry, ZipLimits};
use crate::IngestError;

/// The 7z signature.
pub const MAGIC: &[u8] = b"7z\xBC\xAF\x27\x1C";

/// Decoded size of a compressed 7z header; one entry costs tens of bytes, so this holds
/// archives far above the entry limit.
const MAX_HEADER: u64 = 16 << 20;

fn refuse(reason: String) -> IngestError {
    IngestError::Unrecognized(format!("arquivo 7z recusado: {reason}"))
}

fn unreadable(detail: impl std::fmt::Display) -> IngestError {
    IngestError::Parse {
        format: "7z",
        detail: format!("arquivo 7z ilegível ({detail})"),
    }
}

/// A 7z variable-length number (`7zFormat.txt`, "REAL_UINT64").
fn read_number(buf: &[u8], pos: &mut usize) -> Option<u64> {
    let first = *buf.get(*pos)?;
    *pos += 1;
    let mut mask = 0x80u8;
    let mut value = 0u64;
    for i in 0..8 {
        if first & mask == 0 {
            let high = u64::from(first & mask.wrapping_sub(1));
            return Some(value | (high << (8 * i)));
        }
        let b = *buf.get(*pos)?;
        *pos += 1;
        value |= u64::from(b) << (8 * i);
        mask >>= 1;
    }
    Some(value)
}

fn read_u64_le(buf: &[u8], at: usize) -> Option<u64> {
    let b: [u8; 8] = buf.get(at..at + 8)?.try_into().ok()?;
    Some(u64::from_le_bytes(b))
}

/// The declared decoded size of a compressed ("encoded") header, read straight from the
/// start header and the streams info that describes it: `Ok(None)` for a plain header, an
/// error when the description cannot be read. The library decodes the header into memory
/// before anything else, so the size has to be known first.
fn encoded_header_size(bytes: &[u8]) -> Result<Option<u64>, IngestError> {
    let bad = || unreadable("cabeçalho");
    let offset = read_u64_le(bytes, 12).ok_or_else(bad)?;
    let size = read_u64_le(bytes, 20).ok_or_else(bad)?;
    let start = 32u64.checked_add(offset).ok_or_else(bad)?;
    let end = start.checked_add(size).ok_or_else(bad)?;
    if size == 0 || end > bytes.len() as u64 {
        // A broken start header: the library then searches the end of the file for a plain
        // header, which it reads from the file itself.
        return Ok(None);
    }
    let header = &bytes[start as usize..end as usize];
    const K_END: u8 = 0x00;
    const K_PACK_INFO: u8 = 0x06;
    const K_UNPACK_INFO: u8 = 0x07;
    const K_SIZE: u8 = 0x09;
    const K_CRC: u8 = 0x0A;
    const K_FOLDER: u8 = 0x0B;
    const K_CODERS_UNPACK_SIZE: u8 = 0x0C;
    const K_ENCODED_HEADER: u8 = 0x17;
    if header.first() != Some(&K_ENCODED_HEADER) {
        return Ok(None);
    }
    let mut pos = 1usize;
    let byte = |pos: &mut usize| -> Option<u8> {
        let b = *header.get(*pos)?;
        *pos += 1;
        Some(b)
    };
    let number = |pos: &mut usize| read_number(header, pos);
    let mut largest = 0u64;
    loop {
        match byte(&mut pos).ok_or_else(bad)? {
            K_END => break,
            K_PACK_INFO => {
                number(&mut pos).ok_or_else(bad)?; // pack position
                let streams = number(&mut pos).ok_or_else(bad)?;
                loop {
                    match byte(&mut pos).ok_or_else(bad)? {
                        K_END => break,
                        K_SIZE => {
                            for _ in 0..streams.min(header.len() as u64) {
                                number(&mut pos).ok_or_else(bad)?;
                            }
                        }
                        K_CRC => {
                            // All-defined byte, then a bit vector or nothing, then the CRCs.
                            let all = byte(&mut pos).ok_or_else(bad)?;
                            let n = streams.min(header.len() as u64) as usize;
                            let defined = if all == 0 {
                                let vector =
                                    header.get(pos..pos + n.div_ceil(8)).ok_or_else(bad)?;
                                pos += n.div_ceil(8);
                                (0..n)
                                    .filter(|i| vector[i / 8] & (0x80 >> (i % 8)) != 0)
                                    .count()
                            } else {
                                n
                            };
                            pos += 4 * defined;
                        }
                        _ => return Err(bad()),
                    }
                }
            }
            K_UNPACK_INFO => {
                if byte(&mut pos).ok_or_else(bad)? != K_FOLDER {
                    return Err(bad());
                }
                let folders = number(&mut pos).ok_or_else(bad)?;
                if byte(&mut pos).ok_or_else(bad)? != 0 {
                    // Folders stored outside the header ("external"): not produced by 7-Zip.
                    return Err(bad());
                }
                let mut outputs: Vec<u64> = Vec::new();
                for _ in 0..folders.min(64) {
                    let coders = number(&mut pos).ok_or_else(bad)?;
                    let (mut ins, mut outs) = (0u64, 0u64);
                    for _ in 0..coders.min(64) {
                        let flag = byte(&mut pos).ok_or_else(bad)?;
                        pos += usize::from(flag & 0x0F);
                        if flag & 0x10 != 0 {
                            ins += number(&mut pos).ok_or_else(bad)?;
                            outs += number(&mut pos).ok_or_else(bad)?;
                        } else {
                            ins += 1;
                            outs += 1;
                        }
                        if flag & 0x20 != 0 {
                            let props = number(&mut pos).ok_or_else(bad)?;
                            pos = pos
                                .checked_add(usize::try_from(props).map_err(|_| bad())?)
                                .ok_or_else(bad)?;
                        }
                    }
                    if ins > 64 || outs > 64 || outs == 0 {
                        return Err(bad());
                    }
                    for _ in 0..outs - 1 {
                        number(&mut pos).ok_or_else(bad)?;
                        number(&mut pos).ok_or_else(bad)?;
                    }
                    let packed = ins.checked_sub(outs - 1).ok_or_else(bad)?;
                    if packed > 1 {
                        for _ in 0..packed {
                            number(&mut pos).ok_or_else(bad)?;
                        }
                    }
                    outputs.push(outs);
                }
                if byte(&mut pos).ok_or_else(bad)? != K_CODERS_UNPACK_SIZE {
                    return Err(bad());
                }
                for outs in outputs {
                    for _ in 0..outs {
                        largest = largest.max(number(&mut pos).ok_or_else(bad)?);
                    }
                }
                // The rest of the unpack info (CRCs) does not change the size.
                return Ok(Some(largest));
            }
            _ => return Err(bad()),
        }
    }
    Err(bad())
}

/// Describes why a block cannot be decoded with the methods built in.
fn unsupported_method(archive: &Archive, block: usize) -> Option<String> {
    for coder in &archive.blocks[block].coders {
        let id = coder.encoder_method_id();
        if id == EncoderMethod::ID_AES256_SHA256 {
            return Some("protegido por senha".to_string());
        }
        let known = [
            EncoderMethod::ID_COPY,
            EncoderMethod::ID_LZMA,
            EncoderMethod::ID_LZMA2,
            EncoderMethod::ID_DELTA,
            EncoderMethod::ID_BCJ_X86,
            EncoderMethod::ID_BCJ2,
            EncoderMethod::ID_BCJ_PPC,
            EncoderMethod::ID_BCJ_IA64,
            EncoderMethod::ID_BCJ_ARM,
            EncoderMethod::ID_BCJ_ARM64,
            EncoderMethod::ID_BCJ_ARM_THUMB,
            EncoderMethod::ID_BCJ_SPARC,
            EncoderMethod::ID_BCJ_RISCV,
        ];
        if !known.contains(&id) {
            let name = EncoderMethod::by_id(id).map_or("desconhecido", |m| m.name());
            return Some(format!(
                "método de compressão {name} não suportado (só LZMA e LZMA2)"
            ));
        }
    }
    None
}

/// Opens a 7z archive and reads its entries under the limits. Returns the entries (sorted by
/// path) and pt-BR warnings for what was skipped.
pub fn read_7z(bytes: &[u8], lim: &ZipLimits) -> Result<(Vec<ZipEntry>, Vec<String>), IngestError> {
    if !bytes.starts_with(MAGIC) {
        return Err(IngestError::Unrecognized(
            "sem assinatura de arquivo 7z".to_string(),
        ));
    }
    if let Some(size) = encoded_header_size(bytes)? {
        if size > MAX_HEADER {
            return Err(refuse(format!(
                "cabeçalho descompactado acima de {} MB",
                MAX_HEADER >> 20
            )));
        }
    }
    let password = Password::empty();
    let mut cursor = Cursor::new(bytes);
    let archive = Archive::read(&mut cursor, &password).map_err(|e| match e {
        sevenz_rust2::Error::PasswordRequired | sevenz_rust2::Error::MaybeBadPassword(_) => {
            unreadable("protegido por senha")
        }
        // AES is not built in: an encrypted header is a password-protected archive.
        sevenz_rust2::Error::UnsupportedCompressionMethod(m) if m.contains("AES") => {
            unreadable("protegido por senha")
        }
        sevenz_rust2::Error::UnsupportedCompressionMethod(m) => {
            unreadable(format!("cabeçalho com método {m} não suportado"))
        }
        other => unreadable(other),
    })?;
    let count = archive.files.len();
    if count > lim.max_entries {
        return Err(refuse(format!(
            "{count} entradas (máximo {})",
            lim.max_entries
        )));
    }
    let max_mb = lim.max_uncompressed >> 20;
    let mut declared_total: u64 = 0;
    for file in &archive.files {
        declared_total = declared_total.saturating_add(file.size);
        if declared_total > lim.max_uncompressed {
            return Err(refuse(format!("descompactado acima de {max_mb} MB")));
        }
        let lower = file.name.to_ascii_lowercase();
        if lim.max_depth <= 1
            && [".zip", ".7z"].iter().any(|x| lower.ends_with(x))
            && !file.is_directory
        {
            return Err(refuse("arquivo compactado dentro do 7z".to_string()));
        }
        if safe_path(&file.name).is_none() {
            return Err(refuse("caminho perigoso dentro do 7z".to_string()));
        }
    }
    let size = bytes.len() as u64;
    if size > 0 && declared_total / size > lim.max_ratio {
        return Err(refuse(format!(
            "razão de compressão acima de {}x",
            lim.max_ratio
        )));
    }
    // Per block: its decoded size against its packed streams.
    let firsts = archive.stream_map.block_first_pack_stream_index();
    let packs = archive.pack_sizes();
    for (b, block) in archive.blocks.iter().enumerate() {
        let from = firsts.get(b).copied().unwrap_or(packs.len());
        let to = firsts.get(b + 1).copied().unwrap_or(packs.len());
        let packed: u64 = packs.get(from..to.max(from)).map_or(0, |p| p.iter().sum());
        if packed > 0 && block.get_unpack_size() / packed > lim.max_ratio {
            return Err(refuse(format!(
                "razão de compressão acima de {}x",
                lim.max_ratio
            )));
        }
    }

    let mut entries: Vec<ZipEntry> = Vec::new();
    let mut warnings: Vec<String> = Vec::new();
    let mut read_total: u64 = 0;
    let mut too_big: Option<String> = None;
    for block in 0..archive.blocks.len() {
        if let Some(reason) = unsupported_method(&archive, block) {
            let start = archive
                .stream_map
                .block_first_file_index
                .get(block)
                .copied()
                .unwrap_or(0);
            let names: Vec<String> = archive
                .files
                .iter()
                .enumerate()
                .filter(|(i, f)| {
                    *i >= start
                        && archive
                            .stream_map
                            .file_block_index
                            .get(*i)
                            .copied()
                            .flatten()
                            == Some(block)
                        && !is_noise(&f.name)
                })
                .map(|(_, f)| crate::archive::clean_name(&f.name))
                .collect();
            for name in names {
                warnings.push(format!("{name}: entrada não foi aberta ({reason})"));
            }
            continue;
        }
        let mut source = Cursor::new(bytes);
        // One thread: a run decodes its sources one after another.
        let decoder = BlockDecoder::new(1, block, &archive, &password, &mut source);
        let result = decoder.for_each_entries(&mut |entry, reader| {
            if entry.is_directory || entry.is_anti_item {
                return Ok(true);
            }
            let Some(path) = safe_path(&entry.name) else {
                return Ok(true);
            };
            if is_noise(&path) {
                return Ok(true);
            }
            let declared = entry.size;
            let cap = declared.min(lim.max_uncompressed.saturating_sub(read_total));
            let mut buf = Vec::with_capacity(usize::try_from(cap.min(16 << 20)).unwrap_or(0));
            match reader.take(cap.saturating_add(1)).read_to_end(&mut buf) {
                Ok(n) if n as u64 > cap => {
                    too_big = Some(path);
                    return Ok(false);
                }
                Ok(n) => {
                    read_total = read_total.saturating_add(n as u64);
                    entries.push(ZipEntry { path, bytes: buf });
                }
                Err(e) => {
                    warnings.push(format!("{path}: não foi possível descompactar ({e})"));
                    return Ok(false);
                }
            }
            Ok(true)
        });
        if let Some(path) = too_big {
            return Err(refuse(format!(
                "{path}: conteúdo maior que o declarado ou acima de {max_mb} MB"
            )));
        }
        if let Err(e) = result {
            warnings.push(format!(
                "bloco {} do 7z ilegível ({e}); as entradas seguintes do bloco não foram lidas",
                block + 1
            ));
        }
    }
    // Empty files have no block.
    for (i, file) in archive.files.iter().enumerate() {
        if archive
            .stream_map
            .file_block_index
            .get(i)
            .copied()
            .flatten()
            .is_none()
            && !file.is_directory
            && !file.is_anti_item
        {
            if let Some(path) = safe_path(&file.name).filter(|p| !is_noise(p)) {
                entries.push(ZipEntry {
                    path,
                    bytes: Vec::new(),
                });
            }
        }
    }
    entries.sort_by(|a, b| a.path.cmp(&b.path));
    crate::archive::unique_paths(&mut entries);
    Ok((entries, warnings))
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    #[test]
    fn numbers_follow_the_7z_encoding() {
        let mut pos = 0;
        assert_eq!(read_number(&[0x05], &mut pos), Some(5));
        let mut pos = 0;
        assert_eq!(read_number(&[0x81, 0x02], &mut pos), Some(0x102));
        assert_eq!(pos, 2);
        let mut pos = 0;
        assert_eq!(read_number(&[0xC0, 0x34, 0x12], &mut pos), Some(0x1234));
        let mut pos = 0;
        assert_eq!(
            read_number(&[0xFF, 1, 2, 3, 4, 5, 6, 7, 8], &mut pos),
            Some(0x0807_0605_0403_0201)
        );
        let mut pos = 0;
        assert_eq!(read_number(&[0x81], &mut pos), None);
    }

    #[test]
    fn garbage_is_an_error_not_a_panic() {
        let lim = ZipLimits::default();
        assert!(read_7z(b"nada", &lim).is_err());
        assert!(read_7z(MAGIC, &lim).is_err());
        let mut short = MAGIC.to_vec();
        short.extend_from_slice(&[0, 4, 0, 0, 0, 0]);
        assert!(read_7z(&short, &lim).is_err());
        // A start header pointing at an encoded header that claims a 4 GB decoded size.
        let mut bomb = MAGIC.to_vec();
        bomb.extend_from_slice(&[0, 4]);
        let next = [
            0x17, // encoded header
            0x06, 0x00, 0x01, 0x09, 0x05, 0x00, // pack info: one stream of 5 bytes
            0x07, 0x0B, 0x01, 0x00, // unpack info, one folder
            0x01, 0x21, 0x21, 0x01, 0x18, // one LZMA2 coder with a one-byte property
            0x0C, 0xF1, 0x00, 0x00, 0x00, 0x00, // unpack size 2^32 (five-byte number)
            0x00, 0x00,
        ];
        let mut start = Vec::new();
        start.extend_from_slice(&5u64.to_le_bytes());
        start.extend_from_slice(&(next.len() as u64).to_le_bytes());
        start.extend_from_slice(&crc32(&next).to_le_bytes());
        bomb.extend_from_slice(&crc32(&start).to_le_bytes());
        bomb.extend_from_slice(&start);
        bomb.extend_from_slice(&[0u8; 5]);
        bomb.extend_from_slice(&next);
        assert_eq!(encoded_header_size(&bomb).ok().flatten(), Some(1 << 32));
        assert!(
            matches!(read_7z(&bomb, &lim), Err(IngestError::Unrecognized(m)) if m.contains("cabeçalho descompactado")),
        );
    }

    /// CRC-32 (IEEE), for the hand-built headers above.
    fn crc32(data: &[u8]) -> u32 {
        let mut crc = !0u32;
        for &b in data {
            crc ^= u32::from(b);
            for _ in 0..8 {
                crc = if crc & 1 != 0 {
                    (crc >> 1) ^ 0xEDB8_8320
                } else {
                    crc >> 1
                };
            }
        }
        !crc
    }
}
