//! Property tests: generated CPF/CNPJ/NF-e/boleto values round-trip through the validators,
//! and single-digit corruption is caught wherever the check digit can catch it.

use chrono::NaiveDate;
use dm_validators::boleto::{
    compose_arrecadacao_line, compose_bank_line, parse_boleto_with_reference,
};
use dm_validators::business_days::{add_business_days, business_days_between, is_business_day};
use dm_validators::cnpj::{cnpj_dv, compute_dv as cnpj_compute};
use dm_validators::cpf::{compute_dv as cpf_compute, cpf_dv};
use dm_validators::digits::mod11_sum_2_to_9;
use dm_validators::nfe::{compose_key, nfe_key_dv};
use dm_validators::{expected_factor, Verdict};
use proptest::prelude::*;

fn digits_string(len: usize) -> impl Strategy<Value = String> {
    proptest::collection::vec(0u8..10, len)
        .prop_map(|v| v.iter().map(|d| char::from(b'0' + d)).collect())
}

fn cnpj_chars(len: usize) -> impl Strategy<Value = Vec<u8>> {
    proptest::collection::vec(prop_oneof![0u8..10, 17u8..43], len)
}

fn mutate_digit(s: &str, pos: usize, delta: u8) -> String {
    let mut bytes = s.as_bytes().to_vec();
    let d = bytes[pos] - b'0';
    bytes[pos] = b'0' + (d + delta) % 10;
    String::from_utf8(bytes).unwrap()
}

proptest! {
    #[test]
    fn cpf_round_trip(base in proptest::collection::vec(0u32..10, 9), pos in 0usize..9, delta in 1u8..10) {
        prop_assume!(base.iter().any(|d| *d != base[0]));
        let mut arr = [0u32; 9];
        arr.copy_from_slice(&base);
        let (d1, d2) = cpf_compute(&arr);
        let cpf: String = base.iter().map(ToString::to_string).collect::<String>() + &format!("{d1}{d2}");
        let r = cpf_dv(&cpf);
        prop_assert_eq!(r.verdict, Verdict::Valid);
        prop_assert_eq!(r.digits.as_str(), cpf.as_str());
        let masked = format!("{}.{}.{}-{}", &cpf[..3], &cpf[3..6], &cpf[6..9], &cpf[9..]);
        prop_assert_eq!(cpf_dv(&masked).verdict, Verdict::Valid);
        prop_assert_eq!(cpf_dv(&masked).formatted.unwrap(), masked);
        // a substituted base digit is caught exactly when it changes the check digits
        let bad = mutate_digit(&cpf, pos, delta);
        let mut bad_arr = arr;
        bad_arr[pos] = (arr[pos] + u32::from(delta)) % 10;
        let caught = cpf_compute(&bad_arr) != (d1, d2);
        prop_assert_eq!(cpf_dv(&bad).verdict != Verdict::Valid, caught);
    }

    #[test]
    fn cnpj_round_trip(base in cnpj_chars(12), pos in 0usize..12, delta in 1u8..10) {
        prop_assume!(base.iter().any(|d| *d != base[0]));
        let mut arr = [0u32; 12];
        for (i, v) in base.iter().enumerate() {
            arr[i] = u32::from(*v);
        }
        let (d1, d2) = cnpj_compute(&arr);
        let text: String = base.iter().map(|v| char::from(48 + v)).collect::<String>() + &format!("{d1}{d2}");
        let r = cnpj_dv(&text);
        prop_assert_eq!(r.verdict, Verdict::Valid, "{}", text);
        prop_assert_eq!(cnpj_dv(&text.to_lowercase()).verdict, Verdict::Valid);
        let masked = format!("{}.{}.{}/{}-{}", &text[..2], &text[2..5], &text[5..8], &text[8..12], &text[12..]);
        prop_assert_eq!(cnpj_dv(&masked).verdict, Verdict::Valid);
        if text.as_bytes()[pos].is_ascii_digit() {
            let bad = mutate_digit(&text, pos, delta);
            let mut bad_arr = arr;
            bad_arr[pos] = (arr[pos] + u32::from(delta)) % 10;
            let caught = cnpj_compute(&bad_arr) != (d1, d2);
            prop_assert_eq!(cnpj_dv(&bad).verdict != Verdict::Valid, caught, "{} -> {}", text, bad);
        }
    }

    #[test]
    fn nfe_key_round_trip(
        uf in prop_oneof![Just(35u8), Just(41), Just(33), Just(31), Just(53)],
        year in 0u8..100, month in 1u8..13, cnpj in digits_string(14), model in prop_oneof![Just(55u8), Just(65), Just(57)],
        series in 0u16..1000, number in 1u32..1_000_000_000, emission in 1u8..10, code in 0u32..100_000_000,
        pos in 0usize..43, delta in 1u8..10,
    ) {
        let key = compose_key(uf, year, month, &cnpj, model, series, number, emission, code).unwrap();
        let parsed = nfe_key_dv(&key).unwrap();
        prop_assert_eq!(parsed.verdict, Verdict::Valid, "{:?}", parsed.problems);
        prop_assert_eq!(parsed.number, number);
        prop_assert_eq!(parsed.series, series);
        prop_assert_eq!(parsed.cnpj.as_str(), cnpj.as_str());
        prop_assert_eq!(parsed.numeric_code, code);
        // the zero rule maps remainders 0 and 1 to the same digit, so a substitution can
        // slip through only when it moves the remainder between those two values
        let bad = mutate_digit(&key, pos, delta);
        let bad_parsed = nfe_key_dv(&bad).unwrap();
        let old_rem = mod11_sum_2_to_9(&key[..43]).unwrap() % 11;
        let new_rem = mod11_sum_2_to_9(&bad[..43]).unwrap() % 11;
        if !(old_rem < 2 && new_rem < 2) {
            prop_assert_ne!(bad_parsed.expected_check_digit, bad_parsed.check_digit);
        }
    }

    #[test]
    fn bank_boleto_round_trip(
        // bank codes 8xx collide with the arrecadação product id when only the barcode is
        // given, so the barcode round trip below stays unambiguous with codes below 800
        bank in 1u16..800, factor in 0u16..10_000, value in 0i64..10_000_000_000, free in digits_string(25),
        pos in 0usize..47, delta in 1u8..10,
    ) {
        let bank = format!("{bank:03}");
        let line = compose_bank_line(&bank, 9, factor, value, &free).unwrap();
        let reference = NaiveDate::from_ymd_opt(2026, 11, 4).unwrap();
        let b = parse_boleto_with_reference(&line, reference).unwrap();
        prop_assert!(b.valid, "{:?}", b.problems);
        prop_assert_eq!(b.bank_code.as_deref(), Some(bank.as_str()));
        prop_assert_eq!(b.value_centavos, value);
        prop_assert_eq!(b.due_factor, Some(factor));
        prop_assert_eq!(b.free_field.as_str(), free.as_str());
        if factor >= 1000 {
            prop_assert_eq!(b.due_date_candidates.len(), 2);
            let chosen = b.due_date.unwrap();
            prop_assert_eq!(expected_factor(chosen).map(|(f, _)| f), Some(factor));
        }
        // the barcode re-parses to the same line
        prop_assert_eq!(parse_boleto_with_reference(&b.barcode, reference).unwrap().line, line.clone());
        // any single-digit substitution inside a mod-10 field is caught by that field
        let in_mod10_field = (pos < 9) || (10..20).contains(&pos) || (21..31).contains(&pos);
        if in_mod10_field {
            let bad = mutate_digit(&line, pos, delta);
            let p = parse_boleto_with_reference(&bad, reference).unwrap();
            prop_assert!(!p.valid);
        }
    }

    #[test]
    fn arrecadacao_round_trip(
        segment in prop_oneof![1u8..8, Just(9u8)], identifier in 6u8..10, value in 0i64..100_000_000_000,
        company in digits_string(4), free in digits_string(25), pos in 0usize..48, delta in 1u8..10,
    ) {
        let line = compose_arrecadacao_line(segment, identifier, value, &company, &free).unwrap();
        let reference = NaiveDate::from_ymd_opt(2026, 11, 4).unwrap();
        let b = parse_boleto_with_reference(&line, reference).unwrap();
        prop_assert!(b.valid, "{:?}", b.problems);
        prop_assert_eq!(b.segment, Some(segment));
        prop_assert_eq!(b.value_identifier, Some(identifier));
        prop_assert_eq!(b.value_centavos, value);
        prop_assert_eq!(b.company_id.as_deref(), Some(company.as_str()));
        prop_assert_eq!(b.free_field.as_str(), free.as_str());
        prop_assert_eq!(parse_boleto_with_reference(&b.barcode, reference).unwrap().line, line.clone());
        // mod-10 blocks catch every single substitution of a non-DV digit
        if matches!(identifier, 6 | 7) && pos % 12 != 11 && pos != 2 {
            let bad = mutate_digit(&line, pos, delta);
            let p = parse_boleto_with_reference(&bad, reference).unwrap();
            prop_assert!(!p.valid, "{} -> {}", line, bad);
        }
    }

    #[test]
    fn business_day_arithmetic_is_consistent(days_from_epoch in 0i64..20_000, n in -60i32..60) {
        let from = NaiveDate::from_ymd_opt(2000, 1, 1).unwrap() + chrono::Days::new(days_from_epoch as u64);
        let to = add_business_days(from, n);
        prop_assert!(n == 0 || is_business_day(to));
        // (from, to] counts exactly n business days going forward; going backward the same
        // holds when `from` itself is a business day (it is the one counted at the edge)
        if n > 0 || (n < 0 && is_business_day(from)) {
            prop_assert_eq!(business_days_between(from, to), n);
        }
        prop_assert_eq!(business_days_between(from, from), 0);
        prop_assert_eq!(business_days_between(from, to), -business_days_between(to, from));
    }
}
