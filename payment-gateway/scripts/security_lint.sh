#!/usr/bin/env bash
#
# ChainFX — Static Security Gate (govulncheck + gosec)
#
# Run this locally before pushing, or wire it into CI as a pre-merge gate.
# Covers BOTH Go modules in this repo: the root API/worker module and the
# signer/ module (separate go.mod — see .agents/memory/chainfx-adversarial-audit.md).
#
# govulncheck and gosec are already installed via Nix in this environment.
# If running elsewhere, install them first:
#   go install golang.org/x/vuln/cmd/govulncheck@latest
#   go install github.com/securego/gosec/v2/cmd/gosec@latest
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'

FAILED=0

require_tool() {
  local bin="$1" install_cmd="$2"
  if ! command -v "$bin" &> /dev/null; then
    echo -e "${YELLOW}$bin não encontrado; instalando (${install_cmd})...${NC}"
    eval "$install_cmd"
  fi
}

require_tool govulncheck "go install golang.org/x/vuln/cmd/govulncheck@latest"
require_tool gosec "go install github.com/securego/gosec/v2/cmd/gosec@latest"

export PATH="$PATH:$(go env GOPATH)/bin"

run_module() {
  local module_dir="$1" label="$2"
  echo -e "${YELLOW}—— ${label} (${module_dir}) ——${NC}"

  echo "🔍 govulncheck (dependências vulneráveis)..."
  if ! (cd "$module_dir" && govulncheck ./...); then
    echo -e "${RED}🚨 govulncheck encontrou vulnerabilidades exploráveis em ${label}.${NC}"
    FAILED=1
  fi

  echo "🎯 gosec (padrões de código inseguros — concorrência, pseudo-random, etc)..."
  # G104 (unchecked errors) and G404 (weak RNG) are excluded project-wide as
  # pre-existing style choices, not this gate's concern — every OTHER gosec
  # rule (including crypto, command injection, path traversal, SQL string
  # concat) still fails the build.
  if ! (cd "$module_dir" && gosec -exclude=G104,G404 -quiet ./...); then
    echo -e "${RED}🚨 gosec encontrou padrões de código inseguros em ${label}.${NC}"
    FAILED=1
  fi
  echo ""
}

run_module "$ROOT_DIR" "módulo raiz (API + workers + adversarial)"
run_module "$ROOT_DIR/signer" "módulo signer (custódia, EIP-7702)"

if [[ "$FAILED" -eq 0 ]]; then
  echo -e "${GREEN}✅ Verificações estáticas concluídas sem falhas críticas de segurança de código!${NC}"
else
  echo -e "${RED}❌ Uma ou mais verificações de segurança falharam — corrija antes de mesclar/lançar.${NC}"
fi

exit "$FAILED"
