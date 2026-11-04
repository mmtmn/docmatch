//! Brazilian bank business days (ANBIMA/FEBRABAN national calendar).
//!
//! Holidays: 1º de janeiro, segunda e terça de Carnaval, Sexta-feira Santa, Tiradentes
//! (21/04), Dia do Trabalho (01/05), Corpus Christi, Independência (07/09), Nossa Senhora
//! Aparecida (12/10), Finados (02/11), Proclamação da República (15/11), Consciência Negra
//! (20/11, nacional desde 2024 pela Lei 14.759/2023) e Natal (25/12). Easter follows the
//! anonymous Gregorian computus (Meeus/Jones/Butcher).

use chrono::{Datelike, Days, NaiveDate, Weekday};

/// Easter Sunday of a year (Gregorian computus).
#[allow(clippy::many_single_char_names)] // the algorithm is published with these names
pub fn easter(year: i32) -> Option<NaiveDate> {
    let a = year.rem_euclid(19);
    let b = year.div_euclid(100);
    let c = year.rem_euclid(100);
    let d = b / 4;
    let e = b % 4;
    let f = (b + 8) / 25;
    let g = (b - f + 1) / 3;
    let h = (19 * a + b - d - g + 15) % 30;
    let i = c / 4;
    let k = c % 4;
    let l = (32 + 2 * e + 2 * i - h - k) % 7;
    let m = (a + 11 * h + 22 * l) / 451;
    let month = (h + l - 7 * m + 114) / 31;
    let day = (h + l - 7 * m + 114) % 31 + 1;
    NaiveDate::from_ymd_opt(year, month as u32, day as u32)
}

/// National bank holidays of a year with their pt-BR names, in calendar order.
pub fn holidays(year: i32) -> Vec<(NaiveDate, &'static str)> {
    let mut out = Vec::with_capacity(13);
    let fixed: [(u32, u32, &'static str); 9] = [
        (1, 1, "Confraternização Universal"),
        (4, 21, "Tiradentes"),
        (5, 1, "Dia do Trabalho"),
        (9, 7, "Independência do Brasil"),
        (10, 12, "Nossa Senhora Aparecida"),
        (11, 2, "Finados"),
        (11, 15, "Proclamação da República"),
        (11, 20, "Dia Nacional de Zumbi e da Consciência Negra"),
        (12, 25, "Natal"),
    ];
    for (month, day, name) in fixed {
        if month == 11 && day == 20 && year < 2024 {
            continue;
        }
        if let Some(d) = NaiveDate::from_ymd_opt(year, month, day) {
            out.push((d, name));
        }
    }
    if let Some(easter_sunday) = easter(year) {
        let moveable = [
            (48u64, "Carnaval (segunda-feira)"),
            (47, "Carnaval (terça-feira)"),
            (2, "Sexta-feira Santa"),
        ];
        for (days_before, name) in moveable {
            if let Some(d) = easter_sunday.checked_sub_days(Days::new(days_before)) {
                out.push((d, name));
            }
        }
        if let Some(d) = easter_sunday.checked_add_days(Days::new(60)) {
            out.push((d, "Corpus Christi"));
        }
    }
    out.sort_by_key(|(d, _)| *d);
    out
}

/// The holiday name when the date is a national bank holiday.
pub fn is_holiday(date: NaiveDate) -> Option<&'static str> {
    holidays(date.year())
        .into_iter()
        .find(|(d, _)| *d == date)
        .map(|(_, n)| n)
}

/// True on Monday to Friday when the day is not a holiday.
pub fn is_business_day(date: NaiveDate) -> bool {
    !matches!(date.weekday(), Weekday::Sat | Weekday::Sun) && is_holiday(date).is_none()
}

/// First business day strictly after the date.
pub fn next_business_day(date: NaiveDate) -> NaiveDate {
    let mut d = date;
    loop {
        d = d.succ_opt().unwrap_or(d);
        if is_business_day(d) || d == NaiveDate::MAX {
            return d;
        }
    }
}

/// Last business day strictly before the date.
pub fn previous_business_day(date: NaiveDate) -> NaiveDate {
    let mut d = date;
    loop {
        d = d.pred_opt().unwrap_or(d);
        if is_business_day(d) || d == NaiveDate::MIN {
            return d;
        }
    }
}

/// The date itself when it is a business day, otherwise the next business day. This is what
/// banks do with a due date that falls on a weekend or holiday.
pub fn roll_forward(due_date: NaiveDate) -> NaiveDate {
    if is_business_day(due_date) {
        due_date
    } else {
        next_business_day(due_date)
    }
}

/// Moves `n` business days forward (or backward when negative). Zero returns the date
/// unchanged even when it is not a business day.
pub fn add_business_days(date: NaiveDate, n: i32) -> NaiveDate {
    let mut d = date;
    if n > 0 {
        for _ in 0..n {
            d = next_business_day(d);
        }
    } else {
        for _ in 0..(-n) {
            d = previous_business_day(d);
        }
    }
    d
}

/// Number of business days in `(from, to]`; negative when `to` is before `from`.
///
/// For a business day `to`, `add_business_days(from, business_days_between(from, to)) == to`.
pub fn business_days_between(from: NaiveDate, to: NaiveDate) -> i32 {
    if to >= from {
        let mut count = 0;
        let mut d = from;
        while d < to {
            d = d.succ_opt().unwrap_or(d);
            if is_business_day(d) {
                count += 1;
            }
            if d == NaiveDate::MAX {
                break;
            }
        }
        count
    } else {
        -business_days_between(to, from)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ymd(y: i32, m: u32, d: u32) -> NaiveDate {
        NaiveDate::from_ymd_opt(y, m, d).unwrap()
    }

    #[test]
    fn easter_known_dates() {
        assert_eq!(easter(2024), Some(ymd(2024, 3, 31)));
        assert_eq!(easter(2025), Some(ymd(2025, 4, 20)));
        assert_eq!(easter(2026), Some(ymd(2026, 4, 5)));
        assert_eq!(easter(2027), Some(ymd(2027, 3, 28)));
        assert_eq!(easter(2000), Some(ymd(2000, 4, 23)));
        assert_eq!(easter(2038), Some(ymd(2038, 4, 25)));
    }

    #[test]
    fn moveable_holidays_2025() {
        let h = holidays(2025);
        let names = |d: NaiveDate| h.iter().find(|(x, _)| *x == d).map(|(_, n)| *n);
        assert_eq!(names(ymd(2025, 3, 3)), Some("Carnaval (segunda-feira)"));
        assert_eq!(names(ymd(2025, 3, 4)), Some("Carnaval (terça-feira)"));
        assert_eq!(names(ymd(2025, 4, 18)), Some("Sexta-feira Santa"));
        assert_eq!(names(ymd(2025, 6, 19)), Some("Corpus Christi"));
        assert_eq!(
            names(ymd(2025, 11, 20)),
            Some("Dia Nacional de Zumbi e da Consciência Negra")
        );
        assert_eq!(h.len(), 13);
        assert_eq!(
            holidays(2023).len(),
            12,
            "20/11 só é nacional a partir de 2024"
        );
        assert!(is_holiday(ymd(2023, 11, 20)).is_none());
        assert!(is_holiday(ymd(2024, 11, 20)).is_some());
    }

    #[test]
    fn business_day_checks() {
        assert!(is_business_day(ymd(2025, 11, 4)));
        assert!(!is_business_day(ymd(2025, 11, 1))); // Saturday
        assert!(!is_business_day(ymd(2025, 11, 2))); // Sunday and Finados
        assert!(!is_business_day(ymd(2025, 11, 15))); // Saturday and holiday
        assert!(!is_business_day(ymd(2025, 12, 25)));
        assert!(is_business_day(ymd(2025, 12, 24)));
        assert!(is_business_day(ymd(2025, 12, 31)));
    }

    #[test]
    fn rolling_and_adding() {
        // Friday 2025-10-10 -> next business day Monday 13
        assert_eq!(next_business_day(ymd(2025, 10, 10)), ymd(2025, 10, 13));
        // Saturday 2025-11-01: roll forward skips Sunday (Finados) -> Monday 3
        assert_eq!(roll_forward(ymd(2025, 11, 1)), ymd(2025, 11, 3));
        assert_eq!(roll_forward(ymd(2025, 11, 3)), ymd(2025, 11, 3));
        // Friday 2025-04-18 is Good Friday; previous business day is Thursday 17
        assert_eq!(previous_business_day(ymd(2025, 4, 21)), ymd(2025, 4, 17));
        assert_eq!(add_business_days(ymd(2025, 4, 17), 1), ymd(2025, 4, 22));
        assert_eq!(add_business_days(ymd(2025, 4, 22), -1), ymd(2025, 4, 17));
        assert_eq!(add_business_days(ymd(2025, 4, 22), 0), ymd(2025, 4, 22));
        // D+1 credit over Carnival 2025: Friday 28/02 -> Wednesday 05/03
        assert_eq!(add_business_days(ymd(2025, 2, 28), 1), ymd(2025, 3, 5));
    }

    #[test]
    fn counting() {
        assert_eq!(business_days_between(ymd(2025, 4, 17), ymd(2025, 4, 22)), 1);
        assert_eq!(
            business_days_between(ymd(2025, 4, 22), ymd(2025, 4, 17)),
            -1
        );
        assert_eq!(business_days_between(ymd(2025, 11, 3), ymd(2025, 11, 3)), 0);
        assert_eq!(business_days_between(ymd(2025, 11, 3), ymd(2025, 11, 7)), 4);
        // full week with no holidays
        assert_eq!(business_days_between(ymd(2025, 6, 1), ymd(2025, 6, 8)), 5);
        // whole year of 2025: 365 days - 104 weekend days - holidays on weekdays
        let weekday_holidays = holidays(2025)
            .iter()
            .filter(|(d, _)| !matches!(d.weekday(), Weekday::Sat | Weekday::Sun))
            .count() as i32;
        assert_eq!(
            business_days_between(ymd(2024, 12, 31), ymd(2025, 12, 31)),
            365 - 104 - weekday_holidays
        );
        for from in [ymd(2025, 1, 1), ymd(2025, 2, 28), ymd(2025, 12, 20)] {
            for n in [1, 5, 23] {
                let to = add_business_days(from, n);
                assert_eq!(business_days_between(from, to), n);
            }
        }
    }
}
