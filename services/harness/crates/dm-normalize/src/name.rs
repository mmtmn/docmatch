//! Company-name keys: accent folding, Brazilian corporate abbreviations, legal forms, token
//! sets for reordered names and prefix matching for bank descriptors truncated at ~32 chars.

use serde::{Deserialize, Serialize};

use crate::text::fold_ascii;

/// A normalized name in the forms matching needs.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct NameKey {
    /// Uppercase, no accents, punctuation removed, abbreviations expanded, `S/A` → `SA`.
    pub full: String,
    /// `full` without trailing legal-form tokens (`LTDA`, `ME`, `EPP`, `EIRELI`, `SA`...).
    pub core: String,
    /// Tokens of `core` in order (for prefix matching).
    pub ordered_tokens: Vec<String>,
    /// Sorted, deduplicated tokens of `core` without stop words (for reordered names).
    pub tokens: Vec<String>,
    /// Uppercase, no accents, punctuation removed, nothing expanded (for truncated inputs).
    pub raw: String,
    /// Legal form found at the end, canonical spelling.
    pub legal_form: Option<String>,
}

const STOP_WORDS: [&str; 13] = [
    "DE", "DA", "DO", "DAS", "DOS", "E", "A", "O", "AS", "OS", "EM", "NA", "NO",
];

/// Expands a Brazilian corporate abbreviation (`COM` → `COMERCIO`); unknown tokens come back
/// unchanged. Tokens are expected uppercase and without accents.
pub fn expand_abbreviation(token: &str) -> &str {
    let token = token.trim_end_matches('.');
    match token {
        "COM" | "COMERC" | "COMER" => "COMERCIO",
        "COML" | "COML." => "COMERCIAL",
        "IND" | "INDUST" => "INDUSTRIA",
        "INDL" => "INDUSTRIAL",
        "DIST" | "DISTR" | "DISTRIB" | "DISTRIBUID" => "DISTRIBUIDORA",
        "FRA" | "FRANQ" => "FRANQUIAS",
        "ALIM" | "ALIMENT" => "ALIMENTOS",
        "EMP" => "EMPORIO",
        "PANIF" | "PANIFIC" => "PANIFICADOS",
        "SERV" | "SERVS" | "SERVIC" => "SERVICOS",
        "REPR" | "REPRES" | "REPRESENT" => "REPRESENTACOES",
        "TRANSP" | "TRANSPS" | "TRANSPORT" => "TRANSPORTES",
        "LOG" | "LOGIST" => "LOGISTICA",
        "TEC" | "TECN" | "TECNOL" => "TECNOLOGIA",
        "CONST" | "CONSTR" | "CONSTRUC" => "CONSTRUCOES",
        "ADM" | "ADMIN" | "ADMINIST" => "ADMINISTRACAO",
        "PART" | "PARTIC" | "PARTICIP" => "PARTICIPACOES",
        "IMP" | "IMPORT" => "IMPORTACAO",
        "EXP" | "EXPORT" => "EXPORTACAO",
        "PROD" | "PRODS" => "PRODUTOS",
        "MAT" | "MATS" => "MATERIAIS",
        "EQUIP" | "EQUIPS" => "EQUIPAMENTOS",
        "FARM" => "FARMACIA",
        "SUPERM" | "SUPERMERC" => "SUPERMERCADO",
        "REST" | "RESTAUR" => "RESTAURANTE",
        "PADAR" => "PADARIA",
        "ENG" | "ENGENH" => "ENGENHARIA",
        "CONS" | "CONSULT" => "CONSULTORIA",
        "ASSESS" => "ASSESSORIA",
        "INFO" | "INFORM" => "INFORMATICA",
        "AGROP" | "AGROPEC" => "AGROPECUARIA",
        "EMPREEND" | "EMPREENDIM" => "EMPREENDIMENTOS",
        "CIA" => "COMPANHIA",
        "NAC" | "NACION" => "NACIONAL",
        "INTL" | "INTERN" | "INTERNAC" => "INTERNACIONAL",
        "GRAF" => "GRAFICA",
        "EDIT" => "EDITORA",
        "SOC" => "SOCIEDADE",
        "ASSOC" => "ASSOCIACAO",
        "FUND" => "FUNDACAO",
        "INST" => "INSTITUTO",
        "COOP" => "COOPERATIVA",
        "CLIN" => "CLINICA",
        "LAB" | "LABOR" => "LABORATORIO",
        "HOSP" => "HOSPITAL",
        "ATAC" => "ATACADO",
        "ATACAD" => "ATACADISTA",
        "VAREJ" => "VAREJO",
        "PROM" => "PROMOCOES",
        "EVENT" => "EVENTOS",
        "MEC" => "MECANICA",
        "VEIC" => "VEICULOS",
        "AUTOM" => "AUTOMOTIVA",
        "PEC" => "PECAS",
        "MOV" | "MOVEIS" => "MOVEIS",
        "ELETRON" => "ELETRONICOS",
        "BEBS" | "BEB" => "BEBIDAS",
        "CONF" | "CONFEC" => "CONFECCOES",
        "CALC" => "CALCADOS",
        "TEXT" => "TEXTIL",
        "QUIM" => "QUIMICA",
        "FARMAC" => "FARMACEUTICA",
        "EMB" | "EMBAL" => "EMBALAGENS",
        "PLAST" => "PLASTICOS",
        "METAL" | "METALURG" => "METALURGICA",
        "SEG" => "SEGUROS",
        "CORR" => "CORRETORA",
        "IMOB" => "IMOBILIARIA",
        "INCORP" => "INCORPORADORA",
        "URB" => "URBANIZADORA",
        _ => token,
    }
}

/// Canonical legal form for a token at the end of a name.
fn legal_form(token: &str) -> Option<&'static str> {
    Some(match token {
        "LTDA" | "LIMITADA" | "LTD" | "LT" | "LDA" => "LTDA",
        "ME" => "ME",
        "EPP" => "EPP",
        "EIRELI" => "EIRELI",
        "MEI" => "MEI",
        "SA" => "SA",
        "SS" => "SS",
        "SCP" => "SCP",
        "EI" => "EI",
        "SLU" => "SLU",
        _ => return None,
    })
}

/// Uppercase, accent-free, punctuation-free tokens with `S/A`, `S.A.` and `S A` folded to `SA`.
fn raw_tokens(input: &str) -> Vec<String> {
    let folded = fold_ascii(input);
    let folded = folded
        .replace("S/A", " SA ")
        .replace("S.A.", " SA ")
        .replace("S.A", " SA ")
        .replace("S/S", " SS ")
        .replace('&', " E ")
        .replace(
            [
                '/', '-', ',', ';', ':', '.', '\'', '"', '(', ')', '[', ']', '*', '+', '_',
            ],
            " ",
        );
    let mut tokens: Vec<String> = folded.split_whitespace().map(ToString::to_string).collect();
    // "BANCO DO BRASIL S A" -> SA
    if tokens.len() >= 2 && tokens[tokens.len() - 2] == "S" && tokens[tokens.len() - 1] == "A" {
        tokens.truncate(tokens.len() - 2);
        tokens.push("SA".to_string());
    }
    tokens
}

/// Builds the matching key of a company or person name.
pub fn name_key(input: &str) -> NameKey {
    let raw_tokens = raw_tokens(input);
    let raw = raw_tokens.join(" ");
    let mut expanded: Vec<String> = raw_tokens
        .iter()
        .map(|t| expand_abbreviation(t).to_string())
        .collect();
    let full = expanded.join(" ");
    // Strip legal forms from the end, possibly several ("LTDA ME", "EIRELI EPP").
    // The innermost form wins ("LTDA ME" is an LTDA that is also a micro-enterprise).
    let mut legal: Option<String> = None;
    while let Some(last) = expanded.last() {
        match legal_form(last) {
            Some(form) => {
                legal = Some(form.to_string());
                expanded.pop();
            }
            None => break,
        }
    }
    let core = expanded.join(" ");
    let mut tokens: Vec<String> = expanded
        .iter()
        .filter(|t| !STOP_WORDS.contains(&t.as_str()))
        .cloned()
        .collect();
    tokens.sort_unstable();
    tokens.dedup();
    NameKey {
        full,
        core,
        ordered_tokens: expanded,
        tokens,
        raw,
        legal_form: legal,
    }
}

/// True when a (possibly truncated) descriptor matches a full name: every token but the last
/// is equal after expansion, and the last token is a prefix of its counterpart. Works in both
/// directions, so either side may be the truncated one. Needs at least four characters of
/// evidence to avoid matching on initials alone.
pub fn prefix_match(descriptor: &str, name: &str) -> bool {
    let a = name_key(descriptor);
    let b = name_key(name);
    tokens_prefix(&a.ordered_tokens, &b.ordered_tokens)
        || tokens_prefix(&b.ordered_tokens, &a.ordered_tokens)
}

fn tokens_prefix(short: &[String], long: &[String]) -> bool {
    if short.is_empty() || short.len() > long.len() {
        return false;
    }
    let evidence: usize = short.iter().map(String::len).sum();
    if evidence < 4 {
        return false;
    }
    let n = short.len();
    for i in 0..n - 1 {
        if short[i] != long[i] && !same_after_expansion(&short[i], &long[i]) {
            return false;
        }
    }
    let last_short = &short[n - 1];
    let last_long = &long[n - 1];
    last_long.starts_with(last_short.as_str())
        || expand_abbreviation(last_long).starts_with(last_short.as_str())
        || last_long.starts_with(expand_abbreviation(last_short))
}

fn same_after_expansion(a: &str, b: &str) -> bool {
    expand_abbreviation(a) == expand_abbreviation(b)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn expands_and_strips_legal_forms() {
        let k = name_key("Empório Bela Vista Com. de Alim. Ltda - ME");
        assert_eq!(k.full, "EMPORIO BELA VISTA COMERCIO DE ALIMENTOS LTDA ME");
        assert_eq!(k.core, "EMPORIO BELA VISTA COMERCIO DE ALIMENTOS");
        assert_eq!(k.legal_form.as_deref(), Some("LTDA"));
        assert_eq!(
            k.tokens,
            vec!["ALIMENTOS", "BELA", "COMERCIO", "EMPORIO", "VISTA"]
        );
        assert_eq!(k.raw, "EMPORIO BELA VISTA COM DE ALIM LTDA ME");

        let k = name_key("CIA BRASILEIRA DE DISTR S/A");
        assert_eq!(k.full, "COMPANHIA BRASILEIRA DE DISTRIBUIDORA SA");
        assert_eq!(k.legal_form.as_deref(), Some("SA"));
        assert_eq!(k.core, "COMPANHIA BRASILEIRA DE DISTRIBUIDORA");

        let k = name_key("Padaria do João EIRELI");
        assert_eq!(k.core, "PADARIA DO JOAO");
        assert_eq!(k.legal_form.as_deref(), Some("EIRELI"));

        let k = name_key("BANCO DO BRASIL S A");
        assert_eq!(k.full, "BANCO DO BRASIL SA");

        let k = name_key("Fra Alim Nordeste LT");
        assert_eq!(k.core, "FRANQUIAS ALIMENTOS NORDESTE");
        assert_eq!(k.legal_form.as_deref(), Some("LTDA"));

        let k = name_key("Panif Pão Quente & Cia Ltda");
        assert_eq!(k.core, "PANIFICADOS PAO QUENTE E COMPANHIA");
    }

    #[test]
    fn reordered_names_share_tokens() {
        let a = name_key("BELA VISTA EMPORIO");
        let b = name_key("Emporio Bela Vista");
        assert_eq!(a.tokens, b.tokens);
        assert_ne!(a.core, b.core);
    }

    #[test]
    fn truncated_bank_descriptors() {
        // 32-char bank descriptor vs. the registered name
        assert!(prefix_match(
            "EMPORIO BELA VISTA COM DE ALIM",
            "Empório Bela Vista Comércio de Alimentos Ltda"
        ));
        assert!(prefix_match(
            "EMPORIO BELA VISTA COMERCIO DE A",
            "Empório Bela Vista Comércio de Alimentos Ltda"
        ));
        assert!(prefix_match(
            "Empório Bela Vista Comércio de Alimentos Ltda",
            "EMPORIO BELA VISTA COMERCIO DE A"
        ));
        assert!(prefix_match(
            "DISTR NORDESTE DE BEB",
            "Distribuidora Nordeste de Bebidas S/A"
        ));
        assert!(!prefix_match(
            "EMPORIO BELA VISTA",
            "Empório Boa Vista Ltda"
        ));
        assert!(!prefix_match("E B", "Empório Bela Vista"));
        assert!(!prefix_match("", "Empório Bela Vista"));
        // a descriptor that is a whole-token prefix still matches: the scorer weighs it later
        assert!(prefix_match(
            "PADARIA CENTRAL",
            "Padaria Central Norte Ltda"
        ));
        assert!(prefix_match(
            "PADARIA CENTRAL NO",
            "Padaria Central Norte Ltda"
        ));
        assert!(!prefix_match(
            "PADARIA CENTRAL SUL",
            "Padaria Central Norte Ltda"
        ));
    }

    #[test]
    fn abbreviation_table() {
        assert_eq!(expand_abbreviation("COM"), "COMERCIO");
        assert_eq!(expand_abbreviation("COM."), "COMERCIO");
        assert_eq!(expand_abbreviation("S/A"), "S/A");
        assert_eq!(expand_abbreviation("XYZ"), "XYZ");
        assert_eq!(expand_abbreviation("IND"), "INDUSTRIA");
        assert_eq!(expand_abbreviation("EMP"), "EMPORIO");
    }
}
