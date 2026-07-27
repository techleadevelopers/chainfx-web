package bitcoin

import "errors"

// Erros sentinela da rail Bitcoin.
var (
	ErrDisabled          = errors.New("bitcoin: rail BTC não está habilitada (BTC_ENABLED=false)")
	ErrNoXPub            = errors.New("bitcoin: BTC_XPUB não configurado")
	ErrNoSeed            = errors.New("bitcoin: BTC_ENCRYPTED_SEED não configurado (necessário para assinar)")
	ErrInvalidAddress    = errors.New("bitcoin: endereço Bitcoin inválido")
	ErrWrongNetwork      = errors.New("bitcoin: endereço pertence a uma rede diferente da configurada")
	ErrInsufficientFunds = errors.New("bitcoin: saldo insuficiente para cobrir o valor e a fee")
	ErrDustOutput        = errors.New("bitcoin: valor abaixo do limite de dust (546 sats)")
	ErrNoUTXOs           = errors.New("bitcoin: nenhum UTXO disponível para selecionar")
	ErrFeeTooHigh        = errors.New("bitcoin: fee rate excede BTC_MAX_FEE_RATE_SAT_VB")
	ErrDoubleSpend       = errors.New("bitcoin: UTXO já reservado por outra transação")
	ErrBroadcastUnknown  = errors.New("bitcoin: broadcast enviado mas resultado incerto — não marque como falha definitiva")
	ErrInvalidXPub       = errors.New("bitcoin: xpub inválido ou mal formatado")
	ErrHardenedFromPub   = errors.New("bitcoin: não é possível derivar chave hardenada a partir de xpub")
	ErrIndexExhausted    = errors.New("bitcoin: índice de derivação esgotado")
	ErrMaxSendExceeded   = errors.New("bitcoin: valor excede BTC_MAX_SEND_SATS")
	ErrDailyLimitExceeded   = errors.New("bitcoin: limite diário de envio excedido")
	ErrIdempotencyConflict  = errors.New("bitcoin: idempotency key já usada com payload diferente")
)
