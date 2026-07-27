package bitcoin

import (
	"fmt"
	"sort"
)

// ─── Estimativa de tamanho virtual (vbytes) para P2WPKH ──────────────────────
//
// Cálculo de weight units (BIP141):
//   base_size  = versão(4) + vin_count(1) + vout_count(1) + locktime(4)
//                + por input: txid(32)+vout(4)+scriptSig_len(1)+sequence(4) = 41
//                + por output: value(8)+script_len(1)+script(22)            = 31
//   witness    = marker(1) + flag(1)
//                + por input: items(1) + sig_len(1) + sig(~72) + pk_len(1) + pk(33) = 108
//   weight     = base_size*4 + witness
//   vsize      = ceil(weight / 4)

const (
	txOverheadBase    = 10  // versão 4 + vin_count 1 + vout_count 1 + locktime 4
	txWitnessOverhead = 2   // marker + flag
	p2wpkhInputBase   = 41  // txid 32 + vout 4 + scriptSig 1 (empty) + sequence 4
	p2wpkhInputWit    = 108 // items 1 + sig_len 1 + sig ~72 + pk_len 1 + pk 33
	p2wpkhOutputSize  = 31  // value 8 + script_len 1 + OP_0(1)+PUSH20(1)+hash(20)
)

// EstimateVSize estima o tamanho virtual em vbytes para nInputs entradas e nOutputs saídas P2WPKH.
func EstimateVSize(nInputs, nOutputs int) int {
	baseSize := txOverheadBase + nInputs*p2wpkhInputBase + nOutputs*p2wpkhOutputSize
	witnessSize := txWitnessOverhead + nInputs*p2wpkhInputWit
	weight := baseSize*4 + witnessSize
	return (weight + 3) / 4 // ceil
}

// SelectUTXOs escolhe UTXOs pelo método greedy (maiores primeiro) para cobrir
// amountSats + fee estimada. Retorna os UTXOs selecionados, troco em sats e fee total.
//
// Regras:
//  - Usa somente UTXOs com status "confirmed" (já filtrados pelo repo)
//  - Nunca cria output de troco abaixo de dustLimitSats
//  - Retorna ErrInsufficientFunds se não for possível cobrir amount+fee
func SelectUTXOs(utxos []UTXO, amountSats, feeRateSatVB, dustLimitSats int64) (
	selected []UTXO, changeSats int64, feeSats int64, err error,
) {
	if len(utxos) == 0 {
		return nil, 0, 0, ErrNoUTXOs
	}
	if amountSats <= 0 {
		return nil, 0, 0, fmt.Errorf("coin_select: amountSats deve ser > 0")
	}

	// Ordenar por valor decrescente (maiores primeiro → minimiza entradas)
	sorted := make([]UTXO, len(utxos))
	copy(sorted, utxos)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ValueSats > sorted[j].ValueSats
	})

	var accumulated int64
	var chosen []UTXO

	for _, u := range sorted {
		chosen = append(chosen, u)
		accumulated += u.ValueSats

		// 2 outputs: destino + troco
		nOut := 2
		fee := int64(EstimateVSize(len(chosen), nOut)) * feeRateSatVB
		total := amountSats + fee

		// Se nao cobre destino + troco, ainda pode ser uma transacao valida
		// sem output de troco quando o restante seria dust.
		feeNoChange := int64(EstimateVSize(len(chosen), 1)) * feeRateSatVB
		if accumulated >= amountSats+feeNoChange && accumulated-(amountSats+feeNoChange) < dustLimitSats {
			return chosen, 0, accumulated - amountSats, nil
		}

		if accumulated >= total {
			change := accumulated - total

			// Se o troco for menor que dust, absorvê-lo na fee (sem output de troco)
			if change > 0 && change < dustLimitSats {
				// Re-calcular sem output de troco
				fee = int64(EstimateVSize(len(chosen), 1)) * feeRateSatVB
				total = amountSats + fee
				if accumulated >= total {
					change = accumulated - total
					if change < dustLimitSats {
						change = 0
						fee = accumulated - amountSats
					}
				}
			}

			return chosen, change, fee, nil
		}
	}

	// Não conseguiu cobrir mesmo usando todos os UTXOs
	// Calcular fee com todos para uma mensagem mais precisa
	fee := int64(EstimateVSize(len(sorted), 2)) * feeRateSatVB
	need := amountSats + fee
	return nil, 0, 0, fmt.Errorf("%w: disponível %d sats, necessário %d sats (incluindo fee %d sats)",
		ErrInsufficientFunds, accumulated, need, fee)
}
