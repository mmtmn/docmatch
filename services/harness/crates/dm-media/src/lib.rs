//! Tipos de mídia (áudio, vídeo, imagem) e segmentação (esqueleto; a fase de mídia do plano
//! liga transcrição, OCR e descrição de cenas às Lambdas em sandbox).
#![forbid(unsafe_code)]

use dm_core::{Item, Locator};
use serde::{Deserialize, Serialize};
use thiserror::Error;

/// Errors of the media stage.
#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum MediaError {
    /// Transcription/OCR backend not wired yet.
    #[error("processamento de mídia ainda não implementado: {0}")]
    NotImplemented(&'static str),
    /// The asset is longer or larger than the plan allows.
    #[error("mídia excede o limite: {0}")]
    OverLimit(String),
}

/// Kind of media.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum MediaKind {
    /// Audio file or voice note.
    Audio,
    /// Video.
    Video,
    /// Image, scan or photo of a document.
    Image,
}

/// A media file stored in the media bucket.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct MediaAsset {
    /// Asset id.
    pub id: String,
    /// Kind.
    pub kind: MediaKind,
    /// Object key in the media bucket (never a URL: the bucket comes from the environment).
    pub object_key: String,
    /// Duration for audio/video.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duration_ms: Option<u64>,
    /// Size in bytes.
    pub size_bytes: u64,
    /// SHA-256 of the content, hex.
    pub sha256: String,
}

/// What a segment holds.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SegmentKind {
    /// Speech transcribed to text.
    Speech,
    /// A scene or shot of a video.
    Scene,
    /// A slide or document page shown on screen.
    Slide,
    /// A region of an image with text (OCR).
    TextRegion,
}

/// A segment of an asset with the text extracted from it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Segment {
    /// Asset id.
    pub asset_id: String,
    /// Kind.
    pub kind: SegmentKind,
    /// Where in the asset.
    pub locator: Locator,
    /// Extracted text, when any.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,
    /// Confidence of the extraction `0..=1`.
    pub confidence: f64,
}

/// Transcribes or describes an asset into segments.
pub trait MediaProcessor: Send + Sync {
    /// Produces segments for an asset.
    fn process(&self, asset: &MediaAsset) -> Result<Vec<Segment>, MediaError>;
}

/// Processor that always reports "not implemented" (keeps the pipeline total).
///
/// TODO(fase de mídia): STT (papel `stt`), visão (`vision`) e vídeo (`video`) via Lambdas.
#[derive(Debug, Default, Clone, Copy)]
pub struct NoopProcessor;

impl MediaProcessor for NoopProcessor {
    fn process(&self, asset: &MediaAsset) -> Result<Vec<Segment>, MediaError> {
        Err(MediaError::NotImplemented(match asset.kind {
            MediaKind::Audio => "transcrição de áudio",
            MediaKind::Video => "análise de vídeo",
            MediaKind::Image => "OCR de imagem",
        }))
    }
}

/// Splits a duration into windows of `window_ms` with `overlap_ms` of overlap, for chunked
/// transcription. The last window ends at the duration.
pub fn segment_plan(duration_ms: u64, window_ms: u64, overlap_ms: u64) -> Vec<(u64, u64)> {
    if duration_ms == 0 || window_ms == 0 {
        return Vec::new();
    }
    let step = window_ms.saturating_sub(overlap_ms).max(1);
    let mut out = Vec::new();
    let mut start = 0u64;
    loop {
        let end = (start + window_ms).min(duration_ms);
        out.push((start, end));
        if end >= duration_ms {
            break;
        }
        start += step;
    }
    out
}

/// Converts a segment into an evidence item (`audio_segment`, `video_segment` or
/// `image_region`).
pub fn segment_to_item(segment: &Segment, source_id: &str) -> Item {
    use dm_core::{stable_id, ItemKind, Value};
    let kind = match segment.kind {
        SegmentKind::Speech => ItemKind::AudioSegment,
        SegmentKind::Scene | SegmentKind::Slide => ItemKind::VideoSegment,
        SegmentKind::TextRegion => ItemKind::ImageRegion,
    };
    let mut fields = indexmap::IndexMap::new();
    if let Some(t) = &segment.text {
        fields.insert("text".to_string(), Value::from(t.as_str()));
    }
    fields.insert(
        "asset_id".to_string(),
        Value::from(segment.asset_id.as_str()),
    );
    let locator_json = serde_json::to_string(&segment.locator).unwrap_or_default();
    Item {
        id: stable_id("it", &[source_id, &segment.asset_id, &locator_json]),
        kind,
        source_id: source_id.to_string(),
        locator: segment.locator.clone(),
        fields,
        time: None,
        entity_mentions: Vec::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn plans_windows() {
        assert_eq!(
            segment_plan(10_000, 4_000, 1_000),
            vec![(0, 4_000), (3_000, 7_000), (6_000, 10_000)]
        );
        assert_eq!(segment_plan(3_000, 4_000, 1_000), vec![(0, 3_000)]);
        assert_eq!(segment_plan(0, 4_000, 0), Vec::<(u64, u64)>::new());
        assert_eq!(segment_plan(5, 0, 0), Vec::<(u64, u64)>::new());
        assert_eq!(
            segment_plan(8_000, 4_000, 4_000).len(),
            4_001,
            "overlap equal to the window advances one ms per step"
        );
    }

    #[test]
    fn noop_processor_and_items() {
        let asset = MediaAsset {
            id: "a1".into(),
            kind: MediaKind::Audio,
            object_key: "media/a1.ogg".into(),
            duration_ms: Some(1000),
            size_bytes: 10,
            sha256: "00".into(),
        };
        assert_eq!(
            NoopProcessor.process(&asset),
            Err(MediaError::NotImplemented("transcrição de áudio"))
        );
        let seg = Segment {
            asset_id: "a1".into(),
            kind: SegmentKind::Speech,
            locator: Locator::TimeRange {
                start_ms: 0,
                end_ms: 1000,
            },
            text: Some("pague a NF 12".into()),
            confidence: 0.8,
        };
        let item = segment_to_item(&seg, "src_m");
        assert_eq!(item.kind, dm_core::ItemKind::AudioSegment);
        assert_eq!(item.text(), Some("pague a NF 12"));
        assert_eq!(item.id, segment_to_item(&seg, "src_m").id);
    }
}
