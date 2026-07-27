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

type detailRow struct {
	Label    string
	Value    string
	CopyHint bool
}

func BuildReceiptMessage(to, subject string, r Receipt) Message {
	action := "Compra finalizada"
	intro := "Seu pagamento foi confirmado e seus ativos digitais foram enviados para a wallet informada."
	primary := "Valor pago"
	secondary := "USDT enviado"
	if r.Kind == "sell" {
		action = "Venda finalizada"
		intro = "Seu deposito foi confirmado e o PIX foi enviado para a chave informada."
		primary = "PIX enviado"
		secondary = "USDT recebido"
	}
	when := r.CompletedAt
	if when.IsZero() {
		when = time.Now()
	}
	rows := []detailRow{
		{Label: primary, Value: moneyBRL(firstPositive(r.AmountFiat, r.PayoutFiat))},
		{Label: secondary, Value: fmt.Sprintf("%.8f %s", r.CryptoAmount, fallback(r.Asset, "USDT"))},
		{Label: "Rede", Value: fallback(r.Network, "BSC")},
		{Label: "Cotacao", Value: moneyBRL(r.Rate)},
		{Label: "Taxas ChainFX", Value: moneyBRL(r.FeeFiat)},
		{Label: "Hash/ID", Value: fallback(r.TxHash, "processado"), CopyHint: true},
		{Label: "Ordem", Value: r.OrderID, CopyHint: true},
		{Label: "Concluido em", Value: when.Format("02/01/2006 15:04 MST")},
	}
	if strings.TrimSpace(r.Wallet) != "" {
		rows = append(rows[:3], append([]detailRow{{Label: "Wallet", Value: r.Wallet, CopyHint: true}}, rows[3:]...)...)
	}
	htmlBody := shell(r.Brand, action, intro, "Ver detalhes", orderURL(r.Brand.SiteURL, r.OrderID), rows, "")
	textBody := textReceipt(action, intro, rows)
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

func shell(brand Brand, title, intro, cta, ctaURL string, rows []detailRow, unsubscribe string) string {
	var detail strings.Builder
	if len(rows) > 0 {
		detail.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border:1px solid #e2e5ea;border-radius:10px;border-collapse:separate;border-spacing:0;margin:22px 0 0;background:#ffffff;table-layout:fixed;overflow:hidden;">`)
		for _, row := range rows {
			value := compactValue(row.Value)
			copyHint := ""
			if row.CopyHint {
				copyHint = `<span style="display:inline-block;margin-left:6px;font-size:10px;line-height:1;color:#9aa0a6;border:1px solid #d8dbe2;border-radius:4px;padding:2px 4px;vertical-align:1px;">copy</span>`
			}
			detail.WriteString(`<tr>`)
			detail.WriteString(`<td style="width:34%;padding:11px 14px;border-bottom:1px solid #edf0f3;color:#70757d;font-size:13px;line-height:1.35;vertical-align:top;white-space:nowrap;">` + html.EscapeString(row.Label) + `:</td>`)
			detail.WriteString(`<td style="width:66%;padding:11px 14px;border-bottom:1px solid #edf0f3;color:#202124;font-size:13px;line-height:1.35;font-weight:750;text-align:right;vertical-align:top;word-break:break-word;overflow-wrap:anywhere;max-width:0;">` + html.EscapeString(value) + copyHint + `</td>`)
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
	return `<!doctype html><html><body style="margin:0;background:#f4f5f7;font-family:'Aptos','Segoe UI',Inter,Roboto,Arial,sans-serif;color:#202124;-webkit-font-smoothing:antialiased;">
<div style="max-width:520px;margin:28px auto;padding:0 14px;">
  <div style="background:#fff;border:1px solid #dedfe3;border-radius:16px;overflow:hidden;">
    <div style="padding:34px 30px 30px;">
      <h1 style="font-size:26px;line-height:1.18;margin:0 0 16px;font-weight:850;color:#202124;letter-spacing:0;">` + html.EscapeString(title) + `</h1>
      <p style="font-size:15px;line-height:1.55;color:#686c74;margin:0;white-space:pre-line;">` + html.EscapeString(compactParagraph(intro)) + `</p>
      ` + detail.String() + `
      <div style="text-align:center;margin-top:24px;">
        <a href="` + html.EscapeString(ctaURL) + `" style="display:inline-block;background:#202124;color:#fff;text-decoration:none;border-radius:999px;padding:13px 30px;font-weight:800;font-size:14px;">` + html.EscapeString(cta) + `</a>
      </div>
      <p style="font-size:12px;line-height:1.55;color:#8a8d94;margin:24px 0 0;">` + support + `</p>
    </div>
    <div style="background:#f7f7f9;border-top:1px solid #e4e5e8;padding:28px 30px;">
      <img src="` + html.EscapeString(brand.LogoURL) + `" alt="` + html.EscapeString(brand.Name) + `" style="height:38px;max-width:175px;display:block;margin-bottom:22px;">
      <p style="font-size:12px;color:#8a8d94;margin:0 0 13px;">Help &middot; Terms &middot; Privacy` + unsub + `</p>
      <p style="font-size:11px;color:#8a8d94;margin:0;">&copy; ` + fmt.Sprint(brand.Year) + ` ` + html.EscapeString(brand.Name) + ` &middot; ` + html.EscapeString(brand.Address) + `</p>
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

func compactValue(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) <= 54 {
		return value
	}
	return value[:30] + "..." + value[len(value)-14:]
}

func compactParagraph(value string) string {
	const max = 900
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + "..."
}
