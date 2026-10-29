#!/usr/bin/env bash
#
# Cola entre o lego e a API do DNS da turma. Nao e' chamado a mao: o lego o
# invoca pelo provider exec, uma vez para criar o TXT do desafio e outra para
# apagar.
#
#   $1  present | cleanup
#   $2  fqdn    _acme-challenge.aluno.lab.tonanuvem.com.
#   $3  value   o token do desafio
#
# Por que existe, em vez de o lego falar direto com a API pelo provider
# httpreq: o httpreq so emite basic auth, e a API autentica por x-admin-key
# antes de rotear qualquer coisa. Usar httpreq obrigaria a uma rota
# privilegiada acima do portao de autenticacao. Estas 20 linhas sao mais
# baratas que essa excecao. Ver §6 item 0b do CONTEXTO.md.
#
# Variaveis:
#   DNS_API_URL    endpoint da API      (padrao: https://api.<turma>.<dominio>)
#   DNS_API_KEY    senha da turma       (padrao: aluno)
#   DNS_ADMIN_KEY  senha de instrutor   OBRIGATORIA
#
set -euo pipefail

ACAO="${1:?uso: $0 present|cleanup <fqdn> <valor>}"
FQDN="${2:?falta o fqdn}"
VALOR="${3:?falta o valor}"
# O lego passa exatamente tres argumentos (providers/dns/exec/exec.go). Se um
# dia passar mais, ignoramos em silencio em vez de abortar a emissao.

API="${DNS_API_URL:-https://api.aluno.lab.tonanuvem.com}"
SENHA="${DNS_API_KEY:-aluno}"
ADMIN="${DNS_ADMIN_KEY:?defina DNS_ADMIN_KEY com a senha de instrutor}"

case "$ACAO" in
  present|cleanup) ;;
  *) echo "acao invalida: $ACAO" >&2; exit 1 ;;
esac

# O corpo vai montado com python para escapar o valor corretamente: o token
# do ACME e' base64url e nao precisaria, mas montar JSON com printf e' como
# se quebra silenciosamente no dia em que o formato mudar.
CORPO=$(FQDN="$FQDN" VALOR="$VALOR" python3 -I -c '
import json, os
print(json.dumps({"fqdn": os.environ["FQDN"], "value": os.environ["VALOR"]}))')

RESP=$(mktemp); trap 'rm -f "$RESP"' EXIT
CODIGO=$(curl -sS -o "$RESP" -w '%{http_code}' --max-time 30 \
  -X POST "$API/acme/$ACAO" \
  -H 'content-type: application/json' \
  -H "x-api-key: $SENHA" \
  -H "x-admin-key: $ADMIN" \
  --data "$CORPO") || { echo "falha de rede ao chamar $API/acme/$ACAO" >&2; exit 1; }

# Saida nao-zero aborta a emissao no lego, que e' o que queremos num erro.
case "$CODIGO" in
  2*) exit 0 ;;
  401|403) echo "autenticacao recusada pela API ($CODIGO). Confira DNS_ADMIN_KEY." >&2 ;;
  *)       echo "a API respondeu $CODIGO em /acme/$ACAO" >&2 ;;
esac
head -c 300 "$RESP" >&2; echo >&2
exit 1
