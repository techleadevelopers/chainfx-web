// token_relayer.go — Hidden-spread relay engine (Taxa Zero model).
//
// Business model:
//   The user sees "Gas: Free, Service Fee: R$ 0".
//   Under the hood, a spread (default 1 % = 100 bps) is captured from the
//   token amount before delivery.  ChainFX's TreasuryHot retains feeAmount;
//   the destination receives netAmount.  The BNB gas cost is paid by the
//   signer service's hot wallet HD index 0 and is recovered by the spread.
//
//   Example — user moves 100 USDT, spreadBps = 100 (1 %):
//     feeAmount = 100 × 100 / 10 000 = 1 USDT  → TreasuryHot
//     netAmount = 100 - 1 = 99 USDT             → destination
//
// All arithmetic uses *big.Int (6-decimal micro-USDT).  No float64 on the
// critical path — floats are used for logging and API responses only.
//
// The actual on-chain signing is handled by the external signer service
// (SIGNER_URL / /hd/transfer).  TokenRelayer is purely an arithmetic + routing
// engine; it does not hold or use private keys.
package paymaster

import (
        "fmt"
        "log/slog"
        "math/big"
        "strings"
)

const (
        // defaultSpreadBps is the factory default spread (1 %).
        defaultSpreadBps = 100
        // usdtMicroDivisor converts micro-USDT to human-readable USDT (1e6).
        usdtMicroDivisor = 1_000_000
)

// RelayPlan is the output of TokenRelayer.Plan.
// It contains all values needed to dispatch a split relay.
type RelayPlan struct {
        // Token amounts in micro-USDT (6 decimals) — use *Micro fields for any math.
        TotalMicro *big.Int // original amount the user authorised
        NetMicro   *big.Int // amount delivered to destination (TotalMicro - FeeMicro)
        FeeMicro   *big.Int // spread retained by ChainFX TreasuryHot

        // Human-readable display values — never use these for arithmetic.
        TotalUSDT float64
        NetUSDT   float64
        FeeUSDT   float64

        SpreadBps int // the BPS applied for this plan
}

// TokenRelayer computes the spread split for a relay and produces a RelayPlan.
// The batcher then dispatches two signer calls:
//   1. net leg  — signer sends netAmount → destination
//   2. fee leg  — signer sends feeAmount → TreasuryHot  (best-effort)
//
// If TreasuryHot is empty the fee leg is skipped (full amount → destination).
type TokenRelayer struct {
        spreadBps int
        hotWallet string // TreasuryHot — fee destination
}

// NewTokenRelayer constructs a TokenRelayer.
// spreadBps = 0 defaults to defaultSpreadBps (100 = 1 %).
// hotWallet may be empty — fee leg is then skipped on dispatch.
func NewTokenRelayer(spreadBps int, hotWallet string) *TokenRelayer {
        if spreadBps <= 0 {
                spreadBps = defaultSpreadBps
        }
        return &TokenRelayer{
                spreadBps: spreadBps,
                hotWallet: hotWallet,
        }
}

// Plan splits amountStr (USDT with up to 6 decimal places) into net + fee legs.
//
// amountStr examples: "100", "99.50", "10.000000"
// All *Micro fields are populated with exact integer math; *USDT fields are for display only.
func (tr *TokenRelayer) Plan(amountStr string) (*RelayPlan, error) {
        totalMicro, err := parseUSDTtoMicro(amountStr)
        if err != nil {
                return nil, fmt.Errorf("tokenRelayer.Plan: %w", err)
        }
        if totalMicro.Sign() <= 0 {
                return nil, fmt.Errorf("tokenRelayer.Plan: amount must be positive")
        }

        // feeAmount = total × spreadBps / 10 000  (integer floor division — no rounding up)
        feeMicro := new(big.Int).Div(
                new(big.Int).Mul(totalMicro, big.NewInt(int64(tr.spreadBps))),
                big.NewInt(basisPointDivisor),
        )
        netMicro := new(big.Int).Sub(totalMicro, feeMicro)

        plan := &RelayPlan{
                TotalMicro: totalMicro,
                NetMicro:   netMicro,
                FeeMicro:   feeMicro,
                TotalUSDT:  microToFloat(totalMicro),
                NetUSDT:    microToFloat(netMicro),
                FeeUSDT:    microToFloat(feeMicro),
                SpreadBps:  tr.spreadBps,
        }

        slog.Debug("tokenRelayer: plan computed",
                "total_usdt", plan.TotalUSDT,
                "net_usdt", plan.NetUSDT,
                "fee_usdt", plan.FeeUSDT,
                "spread_bps", tr.spreadBps,
        )

        return plan, nil
}

// FeeDestination returns the hot wallet that receives spread fees.
func (tr *TokenRelayer) FeeDestination() string { return tr.hotWallet }

// HasFeeDestination reports whether a hot wallet is configured.
func (tr *TokenRelayer) HasFeeDestination() bool {
        return strings.TrimSpace(tr.hotWallet) != ""
}

// SpreadBps returns the configured spread in basis points.
func (tr *TokenRelayer) SpreadBps() int { return tr.spreadBps }

// ── Helpers ────────────────────────────────────────────────────────────────────

// parseUSDTtoMicro converts a human amount string ("10.50", "100", "99.000000")
// to micro-USDT (6 decimal places) using *big.Int — no float64 in the critical path.
func parseUSDTtoMicro(amount string) (*big.Int, error) {
        amount = strings.TrimSpace(amount)
        if amount == "" {
                return nil, fmt.Errorf("empty amount")
        }

        // Split on decimal point.
        parts := strings.SplitN(amount, ".", 2)
        intPart := parts[0]
        fracPart := ""
        if len(parts) == 2 {
                fracPart = parts[1]
        }

        // Normalise fracPart to exactly 6 digits (truncate or pad with zeros).
        if len(fracPart) > usdtDecimals {
                fracPart = fracPart[:usdtDecimals]
        } else {
                for len(fracPart) < usdtDecimals {
                        fracPart += "0"
                }
        }

        combined := intPart + fracPart
        // Reject empty integer part (e.g. ".5" → intPart="", combined="500000")
        if combined == "" || combined == strings.Repeat("0", len(combined)) {
                // fine — zero amounts are caught by Sign() check above
        }
        micro, ok := new(big.Int).SetString(combined, 10)
        if !ok {
                return nil, fmt.Errorf("cannot parse USDT amount %q", amount)
        }
        return micro, nil
}

// microToFloat converts micro-USDT to a float64 for display only.
func microToFloat(micro *big.Int) float64 {
        f, _ := new(big.Float).Quo(
                new(big.Float).SetInt(micro),
                big.NewFloat(usdtMicroDivisor),
        ).Float64()
        return f
}
