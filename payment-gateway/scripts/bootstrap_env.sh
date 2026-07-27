#!/usr/bin/env bash

set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

ENV_FILE=".env"

echo -e "${CYAN}🚀 [ChainFX] Inicializando Provisionador de Redes e Faucets...${NC}\n"

if [ ! -f "$ENV_FILE" ]; then
    echo -e "${YELLOW}⚠️  Arquivo $ENV_FILE não encontrado. Criando um novo...${NC}"
    touch "$ENV_FILE"
fi

update_env_var() {
    local key=$1
    local value=$2
    if grep -q "^${key}=" "$ENV_FILE"; then
        sed -i.bak "s|^${key}=.*|${key}=${value}|" "$ENV_FILE" && rm "${ENV_FILE}.bak"
    else
        echo "${key}=${value}" >> "$ENV_FILE"
    fi
}

echo -e "${YELLOW}⚙️  Injetando configurações de rede no ambiente local...${NC}"

update_env_var "POLYGON_AMOY_RPC"      "https://rpc-amoy.polygon.technology/"
update_env_var "POLYGON_AMOY_CHAIN_ID" "80002"
update_env_var "POLYGON_AMOY_SYMBOL"   "POL"
update_env_var "POLYGON_AMOY_EXPLORER" "https://amoy.polygonscan.com/"
update_env_var "POLYGON_AMOY_FAUCET"   "https://faucet.polygon.technology/"

update_env_var "BSC_TESTNET_RPC"      "https://data-seed-prebsc-1-s1.bnbchain.org:8545"
update_env_var "BSC_TESTNET_CHAIN_ID" "97"
update_env_var "BSC_TESTNET_SYMBOL"   "tBNB"
update_env_var "BSC_TESTNET_EXPLORER" "https://testnet.bscscan.com"
update_env_var "BSC_TESTNET_FAUCET"   "https://testnet.bnbchain.org/faucet-smart"

echo -e "${GREEN}✅ Variáveis de rede injetadas com sucesso no arquivo $ENV_FILE!${NC}\n"

echo -e "${YELLOW}🔍 Testando conectividade com as redes (net_version)...${NC}"

echo -n "• Conectando com Polygon Amoy... "
if AMOY_RESP=$(curl -s --max-time 10 -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"net_version","params":[],"id":1}' \
    https://rpc-amoy.polygon.technology/); then
    if echo "$AMOY_RESP" | grep -q "result"; then
        echo -e "${GREEN}[ONLINE]${NC}"
    else
        echo -e "${RED}[ERRO DE RESPOSTA]${NC}"
    fi
else
    echo -e "${RED}[OFFLINE]${NC}"
fi

echo -n "• Conectando com BSC Testnet... "
if BSC_RESP=$(curl -s --max-time 10 -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"net_version","params":[],"id":1}' \
    https://data-seed-prebsc-1-s1.bnbchain.org:8545); then
    if echo "$BSC_RESP" | grep -q "result"; then
        echo -e "${GREEN}[ONLINE]${NC}"
    else
        echo -e "${RED}[ERRO DE RESPOSTA]${NC}"
    fi
else
    echo -e "${RED}[OFFLINE]${NC}"
fi

echo -e "\n${CYAN}🪙  Links rápidos para abastecimento de Faucet:${NC}"
echo -e "• ${YELLOW}Polygon Amoy Faucet:${NC} https://faucet.polygon.technology/"
echo -e "• ${YELLOW}BSC Testnet Faucet:${NC}  https://testnet.bnbchain.org/faucet-smart"

echo -e "\n${GREEN}🚀 Sistema pronto para testes locais com variáveis atualizadas!${NC}"
