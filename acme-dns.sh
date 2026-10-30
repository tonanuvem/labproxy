#!/bin/sh
#
# Cola entre o lego e a API do DNS da turma. Nao e' chamado a mao: o lego o
# invoca pelo provider exec, uma vez para criar o TXT do desafio e outra para
# apagar.
#
#   $1  present | cleanup
#   $2  fqdn    _acme-challenge.aluno.lab.tonanuvem.com.
#   $3  value   o token do desafio
#
# POSIX sh de proposito, sem bash, sem curl e sem python: este script roda
# DENTRO do container do lego, que e' Alpine e so tem sh, o wget do busybox,
# awk e sed. A primeira versao usava os tres e falhava com
# "env: can't execute 'bash'" -- erro que so apareceu num teste ponta a ponta.
#
# Por que exec e nao o provider httpreq: o httpreq so emite basic auth, e a
# API autentica por x-admin-key antes de rotear. Ver §6 item 0b do CONTEXTO.md.
#
# Variaveis:
#   DNS_API_URL    endpoint da API      (padrao: https://api.aluno.lab.tonanuvem.com)
#   DNS_API_KEY    senha da turma       (padrao: aluno)
#   DNS_ADMIN_KEY  senha de instrutor   OBRIGATORIA
#
set -eu

ACAO="${1:-}"; FQDN="${2:-}"; VALOR="${3:-}"
# O lego passa exatamente tres argumentos. Argumentos extras sao ignorados de
# proposito, para uma mudanca na assinatura nao abortar a emissao.

[ -n "$ACAO" ] && [ -n "$FQDN" ] && [ -n "$VALOR" ] || {
  echo "uso: $0 present|cleanup <fqdn> <valor>" >&2; exit 1; }

case "$ACAO" in
  present|cleanup) ;;
  *) echo "acao invalida: $ACAO" >&2; exit 1 ;;
esac

API="${DNS_API_URL:-https://api.aluno.lab.tonanuvem.com}"
SENHA="${DNS_API_KEY:-aluno}"
ADMIN="${DNS_ADMIN_KEY:-}"
[ -n "$ADMIN" ] || { echo "defina DNS_ADMIN_KEY com a senha de instrutor" >&2; exit 1; }

# Escapa o que o JSON exige. Na pratica o fqdn e' um nome de dominio e o
# valor e' base64url, nenhum dos dois precisa -- mas depender disso e' como
# se quebra no dia em que o formato mudar.
escapar() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
CORPO="{\"fqdn\":\"$(escapar "$FQDN")\",\"value\":\"$(escapar "$VALOR")\"}"
URL="$API/acme/$ACAO"

if command -v curl >/dev/null 2>&1; then
  SAIDA=$(curl -sS --max-time 30 -w '\n%{http_code}' -X POST "$URL" \
    -H 'content-type: application/json' \
    -H "x-api-key: $SENHA" -H "x-admin-key: $ADMIN" \
    --data "$CORPO" 2>&1) || { echo "falha de rede em $URL" >&2; exit 1; }
  CODIGO=$(printf '%s' "$SAIDA" | tail -n 1)
  RESTO=$(printf '%s' "$SAIDA" | sed '$d')
  case "$CODIGO" in
    2*) exit 0 ;;
    401|403) echo "autenticacao recusada pela API ($CODIGO)." >&2 ;;
    *)       echo "a API respondeu $CODIGO em /acme/$ACAO" >&2 ;;
  esac
  printf '%s\n' "$RESTO" >&2
  exit 1
fi

# wget do busybox: sai nao-zero em status HTTP de erro, que e' o que faz o
# lego abortar em vez de seguir sem o TXT no ar.
if command -v wget >/dev/null 2>&1; then
  wget -q -O - -T 30 \
    --header='content-type: application/json' \
    --header="x-api-key: $SENHA" \
    --header="x-admin-key: $ADMIN" \
    --post-data="$CORPO" "$URL" >/dev/null && exit 0
  echo "a API recusou ou nao respondeu em /acme/$ACAO" >&2
  exit 1
fi

echo "nem curl nem wget disponiveis neste ambiente" >&2
exit 1
