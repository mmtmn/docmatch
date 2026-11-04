//! Date normalization for pt-BR documents.
//!
//! Accepted: `dd/mm/aaaa`, `dd/mm/aa`, `dd-mm-aaaa`, `dd.mm.aaaa`, ISO `aaaa-mm-dd` (with or
//! without time and zone), `dd/mm hh:mm` and `[dd/mm, hh:mm]` without year (WhatsApp exports,
//! completed with a reference year), month names in Portuguese and English (`5 de março de
//! 2025`, `05/mar/2025`, `5-Mar-25`), compact `aaaammdd` / `ddmmaaaa` (CNAB), and times as
//! `hh:mm`, `hh:mm:ss` or `14h30`.

use chrono::{Datelike, NaiveDate, NaiveTime};
use serde::{Deserialize, Serialize};

use crate::text::{squash_spaces, strip_accents};
use crate::NormalizeError;

/// Context used to complete partial dates.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct DateContext {
    /// Year used for dates without a year (`dd/mm`).
    pub reference_year: Option<i32>,
    /// When the day/month without year falls after this date in the reference year by more
    /// than `max_days_ahead`, the previous year is used (chat exports crossing New Year).
    pub reference_date: Option<NaiveDate>,
    /// Tolerance for `reference_date` (default 31 days when `reference_date` is set).
    pub max_days_ahead: Option<i64>,
}

impl DateContext {
    /// Context with a reference year only.
    pub fn with_year(year: i32) -> Self {
        DateContext {
            reference_year: Some(year),
            reference_date: None,
            max_days_ahead: None,
        }
    }

    /// Context with a reference date (its year is the reference year).
    pub fn with_reference_date(date: NaiveDate) -> Self {
        DateContext {
            reference_year: Some(date.year()),
            reference_date: Some(date),
            max_days_ahead: None,
        }
    }
}

/// Which shape the input had.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum DateFormat {
    /// `dd/mm/aaaa` (any of `/`, `-`, `.`).
    Dmy,
    /// `dd/mm/aa`.
    DmyShortYear,
    /// `aaaa-mm-dd`.
    Iso,
    /// `dd/mm` with the year taken from the context.
    DmNoYear,
    /// Month written as a name.
    MonthName,
    /// Eight digits (`aaaammdd` or `ddmmaaaa`).
    Compact,
}

/// A normalized date with the optional time that came with it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ParsedDate {
    /// The date.
    pub date: NaiveDate,
    /// Time of day when present.
    pub time: Option<NaiveTime>,
    /// True when the year came from the context, not the input.
    pub year_inferred: bool,
    /// Shape of the input.
    pub format: DateFormat,
}

impl ParsedDate {
    /// Date and time, midnight when no time was given.
    pub fn datetime(&self) -> chrono::NaiveDateTime {
        self.date.and_time(self.time.unwrap_or_default())
    }
}

/// Parses a pt-BR date (see the module docs for the accepted shapes).
pub fn date_ptbr(input: &str, ctx: &DateContext) -> Result<ParsedDate, NormalizeError> {
    let err = || NormalizeError::Date(input.to_string());
    let cleaned = pre_clean(input);
    if cleaned.is_empty() {
        return Err(err());
    }
    let (date_part, time) = split_time(&cleaned);
    let date_part = date_part.trim().trim_end_matches(',').trim();
    if date_part.is_empty() {
        return Err(err());
    }
    if let Some(parsed) = parse_iso(date_part) {
        return Ok(ParsedDate {
            date: parsed,
            time,
            year_inferred: false,
            format: DateFormat::Iso,
        });
    }
    if let Some(parsed) = parse_compact(date_part) {
        return Ok(ParsedDate {
            date: parsed,
            time,
            year_inferred: false,
            format: DateFormat::Compact,
        });
    }
    let tokens: Vec<&str> = date_part
        .split(['/', '-', '.', ' '])
        .filter(|t| !t.is_empty() && !t.eq_ignore_ascii_case("de") && !t.eq_ignore_ascii_case("of"))
        .collect();
    match tokens.as_slice() {
        [d, m, y] => {
            // "March 5 2025": month name first, then the day
            let (d, m) = if parse_month(d).is_some_and(|(_, named)| named)
                && m.chars().all(|c| c.is_ascii_digit())
            {
                (m, d)
            } else {
                (d, m)
            };
            let day: u32 = d.parse().map_err(|_| err())?;
            let (month, named) = parse_month(m).ok_or_else(err)?;
            let (year, short) = parse_year(y).ok_or_else(err)?;
            let date = NaiveDate::from_ymd_opt(year, month, day).ok_or_else(err)?;
            let format = if named {
                DateFormat::MonthName
            } else if short {
                DateFormat::DmyShortYear
            } else {
                DateFormat::Dmy
            };
            Ok(ParsedDate {
                date,
                time,
                year_inferred: false,
                format,
            })
        }
        [d, m] => {
            let day: u32 = d.parse().map_err(|_| err())?;
            let (month, named) = parse_month(m).ok_or_else(err)?;
            let year = ctx
                .reference_year
                .ok_or_else(|| NormalizeError::DateNeedsYear(input.to_string()))?;
            let mut date = NaiveDate::from_ymd_opt(year, month, day).ok_or_else(err)?;
            if let Some(reference) = ctx.reference_date {
                let ahead = (date - reference).num_days();
                if ahead > ctx.max_days_ahead.unwrap_or(31) {
                    date = NaiveDate::from_ymd_opt(year - 1, month, day).unwrap_or(date);
                }
            }
            let format = if named {
                DateFormat::MonthName
            } else {
                DateFormat::DmNoYear
            };
            Ok(ParsedDate {
                date,
                time,
                year_inferred: true,
                format,
            })
        }
        _ => Err(err()),
    }
}

/// Removes brackets, quotes, weekday names and the words "às"/"as"/"at" that precede times.
fn pre_clean(input: &str) -> String {
    let s = squash_spaces(input)
        .trim_matches(|c: char| {
            c == '[' || c == ']' || c == '"' || c == '\'' || c == '(' || c == ')'
        })
        .to_string();
    let folded = strip_accents(&s).to_lowercase();
    let mut out = String::with_capacity(s.len());
    for (i, token) in folded.split(' ').enumerate() {
        let token = token.trim_matches(',');
        if token.is_empty() {
            continue;
        }
        if matches!(token, "as" | "at" | "h" | "hs" | "hrs" | "horas") {
            continue;
        }
        if is_weekday(token) {
            continue;
        }
        if i > 0 {
            out.push(' ');
        }
        out.push_str(token);
    }
    squash_spaces(&out)
}

fn is_weekday(token: &str) -> bool {
    matches!(
        token.trim_end_matches(','),
        "segunda"
            | "terca"
            | "quarta"
            | "quinta"
            | "sexta"
            | "sabado"
            | "domingo"
            | "segunda-feira"
            | "terca-feira"
            | "quarta-feira"
            | "quinta-feira"
            | "sexta-feira"
            | "seg"
            | "ter"
            | "qua"
            | "qui"
            | "sex"
            | "sab"
            | "dom"
    )
}

/// Splits a trailing time (`14:30`, `14:30:05`, `14h30`, `14h`) off the date part.
fn split_time(s: &str) -> (&str, Option<NaiveTime>) {
    // ISO "T" separator right after a yyyy-mm-dd date (input is lowercased by then)
    if s.len() > 11 && matches!(s.as_bytes()[10], b't' | b'T') && parse_iso(&s[..10]).is_some() {
        if let Some(t) = parse_time(&s[11..]) {
            return (&s[..10], Some(t));
        }
    }
    let Some(idx) = s.rfind(' ') else {
        return (s, None);
    };
    match parse_time(s[idx + 1..].trim_start_matches(',')) {
        Some(t) => (&s[..idx], Some(t)),
        None => (s, None),
    }
}

fn parse_time(s: &str) -> Option<NaiveTime> {
    // strip zone suffixes: Z, +03:00, -0300
    let s = s.trim();
    let s = s.strip_suffix(['Z', 'z']).unwrap_or(s);
    let s = match s.rfind(['+', '-']) {
        Some(i) if i >= 5 => &s[..i],
        _ => s,
    };
    let s = s.trim_end_matches('h').trim_end_matches("hs");
    let (h, rest) = if let Some((h, rest)) = s.split_once([':', 'h']) {
        (h, rest)
    } else if s.len() <= 2 && s.chars().all(|c| c.is_ascii_digit()) {
        (s, "0")
    } else {
        return None;
    };
    let hour: u32 = h.trim().parse().ok()?;
    let mut parts = rest.split(':');
    let minute: u32 = parts.next().unwrap_or("0").trim().parse().ok()?;
    let second_text = parts.next().unwrap_or("0");
    let second: u32 = second_text
        .split('.')
        .next()
        .unwrap_or("0")
        .trim()
        .parse()
        .ok()?;
    if parts.next().is_some() {
        return None;
    }
    NaiveTime::from_hms_opt(hour, minute, second)
}

fn parse_iso(s: &str) -> Option<NaiveDate> {
    let bytes = s.as_bytes();
    if bytes.len() != 10 || !(bytes[4] == b'-' || bytes[4] == b'/') || bytes[7] != bytes[4] {
        return None;
    }
    let year: i32 = s[0..4].parse().ok()?;
    let month: u32 = s[5..7].parse().ok()?;
    let day: u32 = s[8..10].parse().ok()?;
    NaiveDate::from_ymd_opt(year, month, day)
}

/// `aaaammdd` or `ddmmaaaa`; the plausible-year test decides.
fn parse_compact(s: &str) -> Option<NaiveDate> {
    if s.len() != 8 || !s.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    let as_ymd = (
        s[0..4].parse::<i32>().ok()?,
        s[4..6].parse::<u32>().ok()?,
        s[6..8].parse::<u32>().ok()?,
    );
    let as_dmy = (
        s[4..8].parse::<i32>().ok()?,
        s[2..4].parse::<u32>().ok()?,
        s[0..2].parse::<u32>().ok()?,
    );
    let plausible = |y: i32| (1950..=2100).contains(&y);
    if plausible(as_ymd.0) {
        if let Some(d) = NaiveDate::from_ymd_opt(as_ymd.0, as_ymd.1, as_ymd.2) {
            return Some(d);
        }
    }
    if plausible(as_dmy.0) {
        return NaiveDate::from_ymd_opt(as_dmy.0, as_dmy.1, as_dmy.2);
    }
    None
}

/// Month number from digits or a name (pt-BR or English, full or three letters).
fn parse_month(token: &str) -> Option<(u32, bool)> {
    if token.chars().all(|c| c.is_ascii_digit()) {
        let m: u32 = token.parse().ok()?;
        return (1..=12).contains(&m).then_some((m, false));
    }
    let name = strip_accents(token).to_lowercase();
    let names: [(&str, &str, &str, &str); 12] = [
        ("jan", "janeiro", "january", "jan"),
        ("fev", "fevereiro", "february", "feb"),
        ("mar", "marco", "march", "mar"),
        ("abr", "abril", "april", "apr"),
        ("mai", "maio", "may", "may"),
        ("jun", "junho", "june", "jun"),
        ("jul", "julho", "july", "jul"),
        ("ago", "agosto", "august", "aug"),
        ("set", "setembro", "september", "sep"),
        ("out", "outubro", "october", "oct"),
        ("nov", "novembro", "november", "nov"),
        ("dez", "dezembro", "december", "dec"),
    ];
    for (i, (abbr, full, en, en_abbr)) in names.iter().enumerate() {
        let n = name.trim_end_matches('.');
        if n == *abbr || n == *full || n == *en || n == *en_abbr || n == "sept" {
            return Some((i as u32 + 1, true));
        }
    }
    None
}

/// Year from two or four digits; two-digit years pivot at 69 (`69` → 1969, `68` → 2068).
fn parse_year(token: &str) -> Option<(i32, bool)> {
    if !token.chars().all(|c| c.is_ascii_digit()) {
        return None;
    }
    match token.len() {
        4 => Some((token.parse().ok()?, false)),
        2 => {
            let yy: i32 = token.parse().ok()?;
            Some((if yy >= 69 { 1900 + yy } else { 2000 + yy }, true))
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn d(y: i32, m: u32, day: u32) -> NaiveDate {
        NaiveDate::from_ymd_opt(y, m, day).unwrap()
    }

    fn t(h: u32, m: u32, s: u32) -> NaiveTime {
        NaiveTime::from_hms_opt(h, m, s).unwrap()
    }

    #[test]
    fn common_shapes() {
        let ctx = DateContext::default();
        let cases = [
            ("05/03/2025", d(2025, 3, 5), None, DateFormat::Dmy),
            ("5/3/2025", d(2025, 3, 5), None, DateFormat::Dmy),
            ("05-03-2025", d(2025, 3, 5), None, DateFormat::Dmy),
            ("05.03.2025", d(2025, 3, 5), None, DateFormat::Dmy),
            ("05/03/25", d(2025, 3, 5), None, DateFormat::DmyShortYear),
            ("31/12/99", d(1999, 12, 31), None, DateFormat::DmyShortYear),
            ("2025-03-05", d(2025, 3, 5), None, DateFormat::Iso),
            ("2025/03/05", d(2025, 3, 5), None, DateFormat::Iso),
            (
                "2025-03-05T14:30:00",
                d(2025, 3, 5),
                Some(t(14, 30, 0)),
                DateFormat::Iso,
            ),
            (
                "2025-03-05T14:30:00Z",
                d(2025, 3, 5),
                Some(t(14, 30, 0)),
                DateFormat::Iso,
            ),
            (
                "2025-03-05T14:30:00.123-03:00",
                d(2025, 3, 5),
                Some(t(14, 30, 0)),
                DateFormat::Iso,
            ),
            (
                "2025-03-05 14:30",
                d(2025, 3, 5),
                Some(t(14, 30, 0)),
                DateFormat::Iso,
            ),
            (
                "05/03/2025 14:30",
                d(2025, 3, 5),
                Some(t(14, 30, 0)),
                DateFormat::Dmy,
            ),
            (
                "05/03/2025 14:30:05",
                d(2025, 3, 5),
                Some(t(14, 30, 5)),
                DateFormat::Dmy,
            ),
            (
                "05/03/2025 às 14h30",
                d(2025, 3, 5),
                Some(t(14, 30, 0)),
                DateFormat::Dmy,
            ),
            (
                "05/03/2025, 14:30",
                d(2025, 3, 5),
                Some(t(14, 30, 0)),
                DateFormat::Dmy,
            ),
            (
                "[05/03/2025 14:30:05]",
                d(2025, 3, 5),
                Some(t(14, 30, 5)),
                DateFormat::Dmy,
            ),
            (
                "5 de março de 2025",
                d(2025, 3, 5),
                None,
                DateFormat::MonthName,
            ),
            ("05/mar/2025", d(2025, 3, 5), None, DateFormat::MonthName),
            ("5-Mar-25", d(2025, 3, 5), None, DateFormat::MonthName),
            ("05 MAR 2025", d(2025, 3, 5), None, DateFormat::MonthName),
            (
                "1 de maio de 2025",
                d(2025, 5, 1),
                None,
                DateFormat::MonthName,
            ),
            ("March 5 2025", d(2025, 3, 5), None, DateFormat::MonthName),
            ("20250305", d(2025, 3, 5), None, DateFormat::Compact),
            ("05032025", d(2025, 3, 5), None, DateFormat::Compact),
            (
                "terça-feira, 05/03/2025",
                d(2025, 3, 5),
                None,
                DateFormat::Dmy,
            ),
        ]
        .map(|(i, a, b, c)| (i.to_string(), a, b, c));
        for (input, date, time, format) in cases {
            let p = date_ptbr(&input, &ctx).unwrap_or_else(|e| panic!("{input}: {e}"));
            assert_eq!(p.date, date, "{input}");
            assert_eq!(p.time, time, "{input}");
            assert_eq!(p.format, format, "{input}");
            assert!(!p.year_inferred);
        }
    }

    #[test]
    fn whatsapp_without_year_needs_reference() {
        assert!(matches!(
            date_ptbr("[05/03 14:30]", &DateContext::default()),
            Err(NormalizeError::DateNeedsYear(_))
        ));
        let p = date_ptbr("[05/03 14:30]", &DateContext::with_year(2025)).unwrap();
        assert_eq!(p.date, d(2025, 3, 5));
        assert_eq!(p.time, Some(t(14, 30, 0)));
        assert!(p.year_inferred);
        assert_eq!(p.format, DateFormat::DmNoYear);
        let p = date_ptbr("05/03", &DateContext::with_year(2025)).unwrap();
        assert_eq!(p.date, d(2025, 3, 5));
        // export dated January 2026 mentioning 28/12 -> previous year
        let ctx = DateContext::with_reference_date(d(2026, 1, 10));
        assert_eq!(
            date_ptbr("28/12 09:00", &ctx).unwrap().date,
            d(2025, 12, 28)
        );
        assert_eq!(date_ptbr("15/01 09:00", &ctx).unwrap().date, d(2026, 1, 15));
        assert_eq!(
            date_ptbr("5 de março", &DateContext::with_year(2025))
                .unwrap()
                .date,
            d(2025, 3, 5)
        );
    }

    #[test]
    fn rejects_garbage() {
        let ctx = DateContext::with_year(2025);
        for bad in [
            "",
            "32/01/2025",
            "01/13/2025",
            "2025-13-01",
            "abc",
            "12",
            "05/03/2025 25:61",
            "99999999",
            "1/2/3",
        ] {
            assert!(date_ptbr(bad, &ctx).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn times() {
        assert_eq!(parse_time("14h"), Some(t(14, 0, 0)));
        assert_eq!(parse_time("14h30"), Some(t(14, 30, 0)));
        assert_eq!(parse_time("9:05"), Some(t(9, 5, 0)));
        assert_eq!(parse_time("23:59:59"), Some(t(23, 59, 59)));
        assert_eq!(parse_time("24:00"), None);
        assert_eq!(parse_time("x"), None);
        let p = date_ptbr("05/03/2025 14:30", &DateContext::default()).unwrap();
        assert_eq!(p.datetime(), d(2025, 3, 5).and_hms_opt(14, 30, 0).unwrap());
    }
}
