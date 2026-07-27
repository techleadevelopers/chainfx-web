package mobile

import (
	"net/http"
	"runtime"
	"strings"
	"time"
)

type mobileContentSection struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type mobileFAQSection struct {
	Title string `json:"title"`
	Items []struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	} `json:"items"`
}

func (s *Server) handleContentFAQ(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    "2026.07",
		"updated_at": "2026-07-20",
		"sections": []map[string]any{
			{
				"title": "Conta e acesso",
				"items": []map[string]string{
					{"question": "Como crio minha conta ChainFX?", "answer": "Baixe o app, toque em Criar conta e preencha seus dados. O KYC e solicitado quando voce for enviar cripto, sacar via Pix ou aumentar limites."},
					{"question": "Esqueci meu PIN. O que faco?", "answer": "Acesse Perfil, Seguranca, PIN de acesso e selecione a opcao de redefinicao. A confirmacao exige biometria, e-mail ou validacao de identidade."},
					{"question": "Posso ter mais de uma conta?", "answer": "Cada CPF deve estar vinculado a uma unica conta ChainFX, conforme politicas de seguranca e conformidade."},
				},
			},
			{
				"title": "Compras e vendas",
				"items": []map[string]string{
					{"question": "Como compro criptomoedas com Pix?", "answer": "Acesse Comprar, escolha o ativo e o valor em BRL, confirme a ordem e pague o Pix gerado. O credito ocorre apos confirmacao do pagamento."},
					{"question": "Quanto tempo leva para receber meu Pix na venda?", "answer": "A venda e liquidada apos confirmacao do deposito dos ativos e validacoes de risco. O status fica disponivel em Ordens."},
					{"question": "Ha valor minimo para operar?", "answer": "Os limites atuais da sua conta ficam em Perfil, Taxas e Limites, sempre carregados do backend."},
				},
			},
			{
				"title": "Seguranca",
				"items": []map[string]string{
					{"question": "Meus ativos estao seguros?", "answer": "A ChainFX aplica controles de sessao, PIN, biometria, KYC e monitoramento de dispositivos para reduzir risco operacional."},
					{"question": "O que faco se suspeitar de acesso indevido?", "answer": "Acesse Dispositivos conectados, encerre sessoes desconhecidas, altere seu PIN e abra um chamado no suporte."},
				},
			},
			{
				"title": "Taxas",
				"items": []map[string]string{
					{"question": "Quais sao as taxas da ChainFX?", "answer": "Consulte Perfil, Taxas e Limites. Limites e parametros dinamicos sao retornados pelo backend."},
					{"question": "Existe taxa de custodia ou mensalidade?", "answer": "Nao ha mensalidade padrao. Custos de rede podem existir em envios on-chain e variam conforme a blockchain."},
				},
			},
		},
	})
}

func (s *Server) handleContentDocument(w http.ResponseWriter, r *http.Request) {
	doc := strings.ToLower(strings.TrimSpace(r.PathValue("document")))
	switch doc {
	case "terms":
		writeJSON(w, http.StatusOK, map[string]any{
			"document":   "terms",
			"title":      "Termos de Uso",
			"updated_at": "2026-07-20",
			"sections": []mobileContentSection{
				{"1. Aceitacao dos Termos", "Ao criar uma conta e utilizar os servicos da ChainFX, voce concorda com estes Termos de Uso. Caso nao concorde, interrompa o uso do aplicativo."},
				{"2. Elegibilidade", "Os servicos sao destinados a pessoas capazes e elegiveis conforme regras aplicaveis, politicas internas e validacoes de KYC."},
				{"3. Servicos Oferecidos", "A ChainFX oferece carteira, compra e venda via Pix, swap, DCA, transferencias e recursos relacionados a ativos digitais."},
				{"4. Conta e Seguranca", "Voce e responsavel por proteger credenciais, PIN e dispositivos autorizados. Notifique o suporte em caso de suspeita de acesso indevido."},
				{"5. KYC e Conformidade", "Recursos sensiveis podem exigir verificacao de identidade, analise de risco e cumprimento de normas brasileiras aplicaveis."},
				{"6. Taxas e Tarifas", "Taxas e limites vigentes ficam disponiveis no aplicativo e podem variar conforme produto, ativo, rede, perfil e risco."},
				{"7. Encerramento de Conta", "Voce pode solicitar encerramento pelo suporte. A ChainFX pode restringir contas em caso de fraude, abuso ou obrigacao legal."},
				{"8. Foro e Legislacao", "Estes termos sao regidos pelas leis da Republica Federativa do Brasil, observadas regras de consumo, privacidade e mercado aplicaveis."},
			},
		})
	case "privacy":
		writeJSON(w, http.StatusOK, map[string]any{
			"document":   "privacy",
			"title":      "Politica de Privacidade",
			"updated_at": "2026-07-20",
			"sections": []mobileContentSection{
				{"1. Dados que Coletamos", "Coletamos dados cadastrais, dados de KYC, informacoes financeiras, enderecos de carteira, dados de dispositivo e eventos de uso necessarios ao servico."},
				{"2. Como Usamos seus Dados", "Usamos dados para autenticar usuarios, processar transacoes, prevenir fraude, cumprir obrigacoes legais, prestar suporte e melhorar a plataforma."},
				{"3. Compartilhamento de Dados", "Podemos compartilhar dados com parceiros de KYC, pagamentos, infraestrutura, seguranca, auditoria e autoridades quando houver base legal."},
				{"4. Armazenamento e Seguranca", "Aplicamos controles tecnicos e organizacionais para proteger dados em transito e repouso, mantendo registros pelos prazos legais aplicaveis."},
				{"5. Seus Direitos LGPD", "Voce pode solicitar acesso, correcao, portabilidade, eliminacao e informacoes sobre tratamento pelos canais de suporte da ChainFX."},
				{"6. Atualizacoes", "Esta politica pode ser atualizada. Mudancas relevantes podem ser comunicadas por notificacao, e-mail ou nova versao no aplicativo."},
				{"7. Contato", "Solicitacoes de privacidade podem ser encaminhadas ao suporte ou ao canal privacidade@chainfx.com.br."},
			},
		})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "document not found"})
	}
}

func (s *Server) handleAppAbout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app_name":       "ChainFX Wallet",
		"tagline":        "Seu gateway para o mercado cripto",
		"version":        envOr("MOBILE_APP_VERSION", "1.0.0"),
		"build":          envOr("MOBILE_APP_BUILD", "2026.07.20"),
		"platform":       "iOS / Android",
		"company_name":   envOr("CHAINFX_COMPANY_NAME", "ChainFX Tecnologia Ltda."),
		"company_tax_id": envOr("CHAINFX_COMPANY_TAX_ID", "sob configuracao"),
		"company_city":   envOr("CHAINFX_COMPANY_CITY", "Sao Paulo, SP"),
		"copyright":      "© 2026 ChainFX Tecnologia Ltda. Todos os direitos reservados.",
		"runtime": map[string]any{
			"go_version": runtime.Version(),
			"server_utc": time.Now().UTC(),
		},
		"social_links": []map[string]string{
			{"label": "Twitter / X", "url": envOr("CHAINFX_TWITTER_URL", "https://twitter.com/chainfx")},
			{"label": "Instagram", "url": envOr("CHAINFX_INSTAGRAM_URL", "https://instagram.com/chainfx")},
		},
	})
}
