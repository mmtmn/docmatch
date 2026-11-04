//! PDF text extraction: signature only. Phase 1 of the plan adds `pdf-extract` (or `lopdf`
//! for layout-aware extraction) and `page_region` items with bounding boxes.

use dm_core::Item;

use crate::IngestError;

/// True when the bytes carry the PDF magic header.
pub fn is_pdf(bytes: &[u8]) -> bool {
    bytes.starts_with(b"%PDF")
}

/// Extracts text items (one `page_region` per text block) from a PDF.
///
/// TODO(fase 1): extrair texto por página com pdf-extract e produzir itens `page_region` com
/// bbox; PDFs digitalizados vão para o estágio de mídia (OCR).
pub fn parse_pdf_text(bytes: &[u8], _source_id: &str) -> Result<Vec<Item>, IngestError> {
    if bytes.is_empty() {
        return Err(IngestError::Empty);
    }
    if !is_pdf(bytes) {
        return Err(IngestError::Unrecognized("sem cabeçalho %PDF".to_string()));
    }
    Err(IngestError::NotImplemented("PDF"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn signature_behaviour() {
        assert!(is_pdf(b"%PDF-1.4"));
        assert!(!is_pdf(b"PDF"));
        assert_eq!(parse_pdf_text(b"", "s"), Err(IngestError::Empty));
        assert!(matches!(
            parse_pdf_text(b"nope", "s"),
            Err(IngestError::Unrecognized(_))
        ));
        assert_eq!(
            parse_pdf_text(b"%PDF-1.4\n", "s"),
            Err(IngestError::NotImplemented("PDF"))
        );
    }
}
