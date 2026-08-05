package email

import (
	"fmt"
	"html"
	"strings"
	"time"
)

type Brand struct {
	Name         string
	LogoURL      string
	SiteURL      string
	Address      string
	SupportEmail string
	Year         int
}

type Receipt struct {
	Brand        Brand
	Kind         string
	OrderID      string
	Asset        string
	Network      string
	AmountFiat   float64
	FeeFiat      float64
	PayoutFiat   float64
	CryptoAmount float64
	Rate         float64
	Wallet       string
	TxHash       string
	CompletedAt  time.Time
}

type MarketingCampaign struct {
	Brand       Brand
	Subject     string
	Headline    string
	Intro       string
	Body        string
	CTA         string
	CTAURL      string
	Unsubscribe string
}

type TransactionDetail struct {
	Label    string
	Value    string
	CopyHint bool
}

type OpsOrderCreated struct {
	Brand         Brand
	Subject       string
	Side          string
	Surface       string
	UserName      string
	OrderID       string
	AmountBRL     float64
	CryptoAmount  float64
	Asset         string
	Network       string
	Wallet        string
	PixKey        string
	PaymentMethod string
	CTAURL        string
}

type TransactionReceipt struct {
	Brand   Brand
	Title   string
	Intro   string
	CTA     string
	CTAURL  string
	Details []TransactionDetail
}

type detailRow struct {
	Label    string
	Value    string
	CopyHint bool
	IconURL  string
}

func BuildReceiptMessage(to, subject string, r Receipt) Message {
	asset := strings.ToUpper(fallback(r.Asset, "USDT"))
	action := "Compra finalizada"
	intro := "Seu pagamento foi confirmado e seus ativos digitais foram enviados para a wallet informada."
	primary := "Valor pago"
	secondary := asset + " enviado"
	walletLabel := "Wallet de destino"
	if r.Kind == "sell" {
		action = "Venda finalizada"
		intro = "Seu deposito foi confirmado e o PIX foi enviado para a chave informada."
		primary = "PIX enviado"
		secondary = asset + " recebido"
		walletLabel = "Wallet de deposito"
	}
	when := r.CompletedAt
	if when.IsZero() {
		when = time.Now()
	}
	rows := []detailRow{
		{Label: primary, Value: moneyBRL(firstPositive(r.AmountFiat, r.PayoutFiat)), IconURL: pixIconURL()},
		{Label: secondary, Value: fmt.Sprintf("%.8f %s", r.CryptoAmount, asset), IconURL: assetIconURL(asset)},
		{Label: "Rede", Value: fallback(r.Network, "BSC")},
		{Label: "Cotacao", Value: moneyBRL(r.Rate)},
		{Label: "Taxas ChainFX", Value: moneyBRL(r.FeeFiat)},
		{Label: "Hash/ID", Value: fallback(r.TxHash, "processado"), CopyHint: true},
		{Label: "Ordem", Value: r.OrderID, CopyHint: true},
		{Label: "Concluido em", Value: when.Format("02/01/2006 15:04 MST")},
	}
	if strings.TrimSpace(r.Wallet) != "" {
		rows = append(rows[:3], append([]detailRow{{Label: walletLabel, Value: r.Wallet, CopyHint: true}}, rows[3:]...)...)
	}
	cta := "Ver detalhes"
	ctaURL := orderURL(r.Brand.SiteURL, r.OrderID)
	if scanURL := explorerTxURL(r.Network, r.TxHash); scanURL != "" {
		cta = "Ver Scan"
		ctaURL = scanURL
	}
	htmlBody := shell(r.Brand, action, intro, cta, ctaURL, rows, "")
	textBody := textReceipt(action, intro, rows)
	if ctaURL != "" {
		textBody += "\n" + cta + ": " + ctaURL + "\n"
	}
	return Message{To: to, Subject: subject, TextBody: textBody, HTMLBody: htmlBody}
}

func BuildMarketingMessage(to string, c MarketingCampaign) Message {
	subject := fallback(c.Subject, "Conheca a ChainFX")
	body := strings.TrimSpace(c.Intro)
	if strings.TrimSpace(c.Body) != "" {
		body = strings.TrimSpace(body + "\n\n" + c.Body)
	}
	htmlBody := shell(c.Brand, fallback(c.Headline, subject), body, fallback(c.CTA, "Abrir ChainFX"), fallback(c.CTAURL, c.Brand.SiteURL), nil, c.Unsubscribe)
	textBody := fallback(c.Headline, subject) + "\n\n" + body + "\n\n" + fallback(c.CTAURL, c.Brand.SiteURL)
	if c.Unsubscribe != "" {
		textBody += "\n\nDescadastro: " + c.Unsubscribe
	}
	return Message{To: to, Subject: subject, TextBody: textBody, HTMLBody: htmlBody}
}

func BuildTransactionMessage(to, subject string, r TransactionReceipt) Message {
	rows := make([]detailRow, 0, len(r.Details))
	for _, item := range r.Details {
		if strings.TrimSpace(item.Label) == "" || strings.TrimSpace(item.Value) == "" {
			continue
		}
		rows = append(rows, detailRow{Label: item.Label, Value: item.Value, CopyHint: item.CopyHint})
	}
	title := fallback(r.Title, subject)
	intro := fallback(r.Intro, "Transacao concluida com sucesso na ChainFX.")
	cta := fallback(r.CTA, "Abrir ChainFX")
	ctaURL := fallback(r.CTAURL, r.Brand.SiteURL)
	return Message{
		To:       to,
		Subject:  subject,
		TextBody: textReceipt(title, intro, rows),
		HTMLBody: shell(r.Brand, title, intro, cta, ctaURL, rows, ""),
	}
}

func BuildOpsMessage(to, subject, body string, brand Brand) Message {
	title := fallback(strings.TrimPrefix(subject, "ChainFx: "), subject)
	title = fallback(strings.TrimPrefix(title, "ChainFX: "), subject)
	intro := fallback(body, "Evento operacional registrado na ChainFX.")
	ctaURL := fallback(brand.SiteURL, "https://www.chainfx.store")
	return Message{
		To:       to,
		Subject:  subject,
		TextBody: intro,
		HTMLBody: shell(brand, title, intro, "Abrir painel", ctaURL, nil, ""),
	}
}

func BuildOpsOrderCreatedMessage(to string, r OpsOrderCreated) Message {
	asset := strings.ToUpper(fallback(r.Asset, "USDT"))
	side := strings.ToLower(fallback(r.Side, "sell"))
	sideLabel := "Ordem de venda"
	cryptoLabel := asset + " vendido"
	walletLabel := "Wallet de deposito"
	if side == "buy" {
		sideLabel = "Ordem de compra"
		cryptoLabel = asset + " comprado"
		walletLabel = "Wallet de destino"
	}
	title := greeting(time.Now(), r.UserName)
	intro := "Nova ordem recebida na ChainFX."
	rows := []detailRow{
		{Label: sideLabel, Value: r.OrderID, CopyHint: true},
		{Label: "Valor PIX", Value: moneyBRL(r.AmountBRL), IconURL: pixIconURL()},
		{Label: cryptoLabel, Value: fmt.Sprintf("%.8f %s", r.CryptoAmount, asset), IconURL: assetIconURL(asset)},
		{Label: "Rede", Value: fallback(r.Network, "-")},
	}
	if strings.TrimSpace(r.Wallet) != "" {
		rows = append(rows, detailRow{Label: walletLabel, Value: r.Wallet, CopyHint: true})
	}
	if strings.TrimSpace(r.PixKey) != "" {
		rows = append(rows, detailRow{Label: "Chave PIX", Value: r.PixKey, CopyHint: true, IconURL: pixIconURL()})
	}
	cta := "Abrir painel"
	if strings.EqualFold(strings.TrimSpace(r.Surface), "mobile") {
		cta = "Abrir app"
	}
	ctaURL := fallback(r.CTAURL, r.Brand.SiteURL)
	subject := fallback(r.Subject, "ChainFX: nova ordem criada")
	return Message{
		To:       to,
		Subject:  subject,
		TextBody: textReceipt(title, intro, rows),
		HTMLBody: shell(r.Brand, title, intro, cta, ctaURL, rows, ""),
	}
}

func shell(brand Brand, title, intro, cta, ctaURL string, rows []detailRow, unsubscribe string) string {
	var detail strings.Builder
	if len(rows) > 0 {
		detail.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border:1px solid rgba(255,255,255,.10);border-radius:10px;border-collapse:separate;border-spacing:0;margin:22px 0 0;background:#171717;table-layout:fixed;overflow:hidden;">`)
		for _, row := range rows {
			value := compactValue(row.Value, row.CopyHint)
			copyHint := ""
			if row.CopyHint {
				copyHint = `<span style="display:inline-block;margin-left:6px;font-size:10px;line-height:1;color:#777c7f;border:1px solid rgba(255,255,255,.12);border-radius:4px;padding:2px 4px;vertical-align:1px;">copy</span>`
			}
			icon := ""
			if strings.TrimSpace(row.IconURL) != "" {
				icon = `<img src="` + html.EscapeString(row.IconURL) + `" alt="" width="18" height="18" style="width:18px;height:18px;border-radius:50%;vertical-align:-4px;margin-right:8px;">`
			}
			detail.WriteString(`<tr>`)
			detail.WriteString(`<td style="width:38%;padding:11px 14px;border-bottom:1px solid rgba(255,255,255,.07);color:#777c7f;font-size:13px;line-height:1.35;vertical-align:top;white-space:nowrap;">` + icon + html.EscapeString(row.Label) + `:</td>`)
			detail.WriteString(`<td style="width:62%;padding:11px 14px;border-bottom:1px solid rgba(255,255,255,.07);color:#f4f4f5;font-size:13px;line-height:1.35;font-weight:750;text-align:right;vertical-align:top;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:0;">` + html.EscapeString(value) + copyHint + `</td>`)
			detail.WriteString(`</tr>`)
		}
		detail.WriteString(`</table>`)
	}
	unsub := ""
	if unsubscribe != "" {
		unsub = ` &middot; <a href="` + html.EscapeString(unsubscribe) + `" style="color:#8a8d94;text-decoration:none;">Unsubscribe</a>`
	}
	support := ""
	if brand.SupportEmail != "" {
		support = `Reply to this email or contact ` + html.EscapeString(brand.SupportEmail) + `.`
	}
	return `<!doctype html><html><body style="margin:0;background:#111111;font-family:'Aptos','Segoe UI',Inter,Roboto,Arial,sans-serif;color:#f4f4f5;-webkit-font-smoothing:antialiased;">
<div style="max-width:520px;margin:28px auto;padding:0 14px;">
  <div style="background:#202020;border:1px solid rgba(255,255,255,.08);border-radius:16px;overflow:hidden;">
    <div style="padding:34px 30px 30px;">
      <h1 style="font-size:21px;line-height:1.18;margin:0 0 16px;font-weight:850;color:#f4f4f5;letter-spacing:0;">` + html.EscapeString(title) + `</h1>
      <p style="font-size:15px;line-height:1.55;color:#a1a1aa;margin:0;white-space:pre-line;">` + html.EscapeString(compactParagraph(intro)) + `</p>
      ` + detail.String() + `
      <div style="text-align:center;margin-top:24px;">
        <a href="` + html.EscapeString(ctaURL) + `" style="display:inline-block;background:#f4f4f5;color:#111111;text-decoration:none;border-radius:999px;padding:13px 30px;font-weight:800;font-size:14px;">` + html.EscapeString(cta) + `</a>
      </div>
      <p style="font-size:12px;line-height:1.55;color:#777c7f;margin:24px 0 0;">` + support + `</p>
    </div>
    <div style="background:#171717;border-top:1px solid rgba(255,255,255,.08);padding:20px 30px;">
      <img src="` + html.EscapeString(brand.LogoURL) + `" alt="` + html.EscapeString(brand.Name) + `" style="height:53px;max-width:245px;display:block;margin-bottom:14px;">
      <p style="font-size:12px;color:#777c7f;margin:0 0 13px;">Help &middot; Terms &middot; Privacy` + unsub + `</p>
      <p style="font-size:11px;color:#777c7f;margin:0;">&copy; ` + fmt.Sprint(brand.Year) + ` ` + html.EscapeString(brand.Name) + ` &middot; ` + html.EscapeString(brand.Address) + `</p>
    </div>
  </div>
</div>
</body></html>`
}

func textReceipt(title, intro string, rows []detailRow) string {
	var b strings.Builder
	b.WriteString(title + "\n\n" + intro + "\n\n")
	for _, row := range rows {
		b.WriteString(row.Label + ": " + row.Value + "\n")
	}
	return b.String()
}

func orderURL(siteURL, orderID string) string {
	siteURL = strings.TrimRight(fallback(siteURL, "https://www.chainfx.store"), "/")
	if orderID == "" {
		return siteURL
	}
	return siteURL + "/?order=" + orderID
}

func moneyBRL(v float64) string {
	if v <= 0 {
		return "-"
	}
	return fmt.Sprintf("R$ %.2f", v)
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func fallback(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func compactValue(value string, copyHint bool) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if copyHint && len(value) > 24 {
		return value[:10] + "....." + value[len(value)-6:]
	}
	if len(value) <= 54 {
		return value
	}
	return value[:30] + "....." + value[len(value)-14:]
}

func compactParagraph(value string) string {
	const max = 900
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + "..."
}

func greeting(now time.Time, name string) string {
	prefix := "Boa noite"
	if hour := now.Hour(); hour >= 5 && hour < 12 {
		prefix = "Bom dia"
	} else if hour >= 12 && hour < 18 {
		prefix = "Boa tarde"
	}
	if strings.TrimSpace(name) == "" {
		return prefix
	}
	first := strings.Fields(strings.TrimSpace(name))[0]
	return prefix + ", " + first
}

func pixIconURL() string {
	return "https://res.cloudinary.com/limpeja/image/upload/v1785713746/pix_omgow6.png"
}

func assetIconURL(asset string) string {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "USDT":
		return "https://tether.to/images/logoCircle.png"
	case "BTC":
		return "https://cdn.jsdelivr.net/gh/atomiclabs/cryptocurrency-icons/32/color/btc.png"
	case "ETH":
		return "https://cdn.jsdelivr.net/gh/atomiclabs/cryptocurrency-icons/32/color/eth.png"
	case "BNB":
		return "https://cdn.jsdelivr.net/gh/atomiclabs/cryptocurrency-icons/32/color/bnb.png"
	default:
		return ""
	}
}

func explorerTxURL(network, txHash string) string {
	txHash = strings.TrimSpace(txHash)
	if !looksLikeChainTxHash(txHash) {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "BSC", "BEP20", "BNB", "BINANCE":
		return "https://bscscan.com/tx/" + txHash
	case "POLYGON", "MATIC":
		return "https://polygonscan.com/tx/" + txHash
	case "ETH", "ETHEREUM", "ERC20":
		return "https://etherscan.io/tx/" + txHash
	case "BASE":
		return "https://basescan.org/tx/" + txHash
	case "ARBITRUM", "ARB":
		return "https://arbiscan.io/tx/" + txHash
	case "BTC", "BITCOIN":
		return "https://mempool.space/tx/" + txHash
	default:
		return ""
	}
}

func looksLikeChainTxHash(txHash string) bool {
	txHash = strings.TrimSpace(txHash)
	if strings.HasPrefix(strings.ToLower(txHash), "0x") {
		return len(txHash) == 66 && isHex(txHash[2:])
	}
	return len(txHash) == 64 && isHex(txHash)
}

func isHex(value string) bool {
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return value != ""
}
