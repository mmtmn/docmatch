//! Payment status words and flags (`Aberto`, `Pago`, `paid_flag=N`, `S`, `Baixado`...).

use serde::{Deserialize, Serialize};

use crate::text::fold_ascii;

/// Normalized payment status of a title or installment.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PaymentStatus {
    /// Not paid yet (aberto, pendente, vencido, a pagar, flag N).
    Open,
    /// Paid (pago, baixado, liquidado, quitado, compensado, flag S).
    Paid,
    /// Partially paid.
    Partial,
    /// Cancelled, reversed or voided.
    Cancelled,
    /// Not recognizable.
    Unknown,
}

/// Maps a status cell to a [`PaymentStatus`]. Flags (`S`/`N`, `Y`/`N`, `1`/`0`, `sim`/`não`)
/// are read as "paid?".
pub fn status_ptbr(input: &str) -> PaymentStatus {
    let folded = fold_ascii(input);
    let folded = folded.trim_matches(|c: char| !c.is_ascii_alphanumeric());
    match folded {
        "" => PaymentStatus::Unknown,
        "PAGO" | "PAGA" | "PG" | "BAIXADO" | "BAIXADA" | "BAIXA" | "LIQUIDADO" | "LIQUIDADA"
        | "LIQ" | "QUITADO" | "QUITADA" | "COMPENSADO" | "COMPENSADA" | "RECEBIDO" | "RECEBIDA"
        | "CONCILIADO" | "CONCILIADA" | "S" | "SIM" | "Y" | "YES" | "TRUE" | "1" | "PAID"
        | "SETTLED" | "CLEARED" | "OK" | "CONCLUIDO" | "CONCLUIDA" | "EFETIVADO" | "EFETUADO"
        | "PAGAMENTO EFETUADO" => PaymentStatus::Paid,
        "ABERTO" | "ABERTA" | "EM ABERTO" | "A PAGAR" | "A RECEBER" | "PENDENTE" | "VENCIDO"
        | "VENCIDA" | "A VENCER" | "NAO PAGO" | "NAO PAGA" | "N" | "NAO" | "NO" | "FALSE" | "0"
        | "OPEN" | "UNPAID" | "PENDING" | "OVERDUE" | "DUE" | "EM ATRASO" | "ATRASADO"
        | "ATRASADA" | "PROGRAMADO" | "AGENDADO" | "AGENDADA" | "EMITIDO" | "EMITIDA" => {
            PaymentStatus::Open
        }
        "PARCIAL" | "PAGO PARCIALMENTE" | "PAGO PARCIAL" | "PARCIALMENTE PAGO" | "PARTIAL"
        | "PARTIALLY PAID" => PaymentStatus::Partial,
        "CANCELADO" | "CANCELADA" | "ESTORNADO" | "ESTORNADA" | "ANULADO" | "ANULADA"
        | "CANCELLED" | "CANCELED" | "VOID" | "VOIDED" | "REVERSED" | "DEVOLVIDO" | "DEVOLVIDA"
        | "INUTILIZADO" | "INUTILIZADA" => PaymentStatus::Cancelled,
        other => {
            // Compound cells such as "Pago em 05/03/2025" or "Vencido há 3 dias".
            let first = other.split(' ').next().unwrap_or("");
            match first {
                "PAGO" | "PAGA" | "BAIXADO" | "LIQUIDADO" | "QUITADO" | "COMPENSADO" => {
                    PaymentStatus::Paid
                }
                "ABERTO" | "VENCIDO" | "VENCIDA" | "PENDENTE" => PaymentStatus::Open,
                "CANCELADO" | "CANCELADA" | "ESTORNADO" => PaymentStatus::Cancelled,
                "PARCIAL" | "PARCIALMENTE" => PaymentStatus::Partial,
                _ => PaymentStatus::Unknown,
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn aberto_vs_paid_flag() {
        assert_eq!(status_ptbr("Aberto"), PaymentStatus::Open);
        assert_eq!(status_ptbr("N"), PaymentStatus::Open);
        assert_eq!(status_ptbr("S"), PaymentStatus::Paid);
        assert_eq!(status_ptbr("Pago"), PaymentStatus::Paid);
        assert_eq!(status_ptbr(" baixado "), PaymentStatus::Paid);
        assert_eq!(status_ptbr("Liquidado"), PaymentStatus::Paid);
        assert_eq!(status_ptbr("Em aberto"), PaymentStatus::Open);
        assert_eq!(status_ptbr("Vencido"), PaymentStatus::Open);
        assert_eq!(status_ptbr("Não pago"), PaymentStatus::Open);
        assert_eq!(status_ptbr("0"), PaymentStatus::Open);
        assert_eq!(status_ptbr("1"), PaymentStatus::Paid);
        assert_eq!(status_ptbr("Cancelado"), PaymentStatus::Cancelled);
        assert_eq!(status_ptbr("Estornado"), PaymentStatus::Cancelled);
        assert_eq!(status_ptbr("Pago parcialmente"), PaymentStatus::Partial);
        assert_eq!(status_ptbr("Pago em 05/03/2025"), PaymentStatus::Paid);
        assert_eq!(status_ptbr("Vencido há 3 dias"), PaymentStatus::Open);
        assert_eq!(status_ptbr("xyz"), PaymentStatus::Unknown);
        assert_eq!(status_ptbr(""), PaymentStatus::Unknown);
        assert_eq!(status_ptbr("paid"), PaymentStatus::Paid);
        assert_eq!(status_ptbr("open"), PaymentStatus::Open);
    }
}
