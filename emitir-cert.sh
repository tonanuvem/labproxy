#!/usr/bin/env bash
#
# Emite o certificado da turma, do lado do INSTRUTOR. Nao roda na VM do aluno.
#
#   ./emitir-cert.sh                 # staging da Let's Encrypt (de mentira)
#   ./emitir-cert.sh --producao      # certificado de verdade
#   ./emitir-cert.sh --producao turma2
#
# Desde o §3.5 as aplicacoes sao HTTP e so o painel e' HTTPS, entao o
# certificado precisa de UM nome: o curinga da turma. Ele cobre o painel de
# todos os alunos — joao.<turma>..., maria.<turma>... — e nao muda quando
# entra aluno novo. Uma emissao por turma a cada 90 dias.
#
# Dois caminhos para criar o TXT do desafio:
#
#   DNS_ADMIN_KEY definida -> fala com a API da turma pelo acme-dns.sh.
#                             Dispensa credencial AWS e roda de qualquer
#                             maquina. Exige a rota /acme na API.
#   sem ela                -> escreve direto no Route 53. Exige credencial da
#                             conta da turma, que no AWS Academy expira em
#                             poucas horas.
#
# Requisito comum: Docker, para rodar o lego sem instalar nada.
#
set -euo pipefail

PRODUCAO=0
[ "${1:-}" = "--producao" ] && { PRODUCAO=1; shift; }
TURMA="${1:-${DNS_TURMA:-aluno}}"
DOMINIO="${DNS_DOMINIO:-lab.tonanuvem.com}"
EMAIL="${ACME_EMAIL:-instrutor@tonanuvem.com}"
ZONA="${TURMA}.${DOMINIO}"
NOME="*.${ZONA}"
SAIDA="${SAIDA:-$PWD/cert-${TURMA}}"
LEGO_IMG="${LEGO_IMG:-goacme/lego:v5.6.0}"

N=$'\033[1m'; F=$'\033[0m'; V=$'\033[32m'; A=$'\033[33m'; R=$'\033[31m'
ok()    { printf '  %s✓%s %s\n' "$V" "$F" "$*"; }
aviso() { printf '  %s!%s %s\n' "$A" "$F" "$*"; }
erro()  { printf '  %s✗%s %s\n' "$R" "$F" "$*" >&2; exit 1; }

command -v docker >/dev/null || erro "docker nao encontrado."

VIA_API=0
if [ -n "${DNS_ADMIN_KEY:-}" ]; then
  VIA_API=1
  ok "desafio pela API da turma (dispensa credencial AWS)"
else
  command -v aws >/dev/null || erro "sem DNS_ADMIN_KEY e sem aws cli: nao ha como criar o TXT."
  CONTA=$(aws sts get-caller-identity --query Account --output text 2>/dev/null) \
    || erro "credencial AWS ausente ou expirada.
       No CloudShell da conta da turma ela ja existe. Alternativa sem AWS:
       exporte DNS_ADMIN_KEY com a senha de instrutor e use a API da turma."
  ok "conta AWS: $CONTA"

  ZONA_ID=$(aws route53 list-hosted-zones \
    --query "HostedZones[?Name=='${ZONA}.'].Id | [0]" --output text 2>/dev/null)
  [ -n "$ZONA_ID" ] && [ "$ZONA_ID" != "None" ] \
    || erro "zona '${ZONA}' nao encontrada nesta conta. Confira o nome da turma."
  ZONA_ID="${ZONA_ID##*/}"
  ok "zona: ${ZONA} (${ZONA_ID})"
fi

if [ "$PRODUCAO" = "1" ]; then
  SERVIDOR="https://acme-v02.api.letsencrypt.org/directory"
  aviso "emitindo de ${N}VERDADE${F}. O limite e' 5 por conjunto identico de"
  printf '    nomes a cada 7 dias, sem excecao. Teste antes sem --producao.\n'
else
  SERVIDOR="https://acme-staging-v02.api.letsencrypt.org/directory"
  ok "usando o ${N}staging${F} (o certificado nao sera confiavel; so valida o fluxo)"
fi

mkdir -p "$SAIDA"
printf '\n  Pedindo %s\n\n' "${N}${NOME}${F}"

# As credenciais vao num env-file temporario: funciona igual no CloudShell
# (credencial da sessao) e com perfil local, e nao aparece na linha de comando.
ENVFILE=$(mktemp); trap 'rm -f "$ENVFILE"' EXIT
chmod 600 "$ENVFILE"

if [ "$VIA_API" = "1" ]; then
  # EXEC_MODE fica FORA do ambiente de proposito: a comparacao do lego e'
  # exata contra "RAW", e no modo padrao o script recebe o fqdn e o valor ja
  # prontos, em vez do token cru.
  { printf 'EXEC_PATH=/data/acme-dns.sh\n'
    printf 'DNS_API_URL=%s\n'   "${DNS_API_URL:-https://api.${ZONA}}"
    printf 'DNS_API_KEY=%s\n'   "${DNS_API_KEY:-aluno}"
    printf 'DNS_ADMIN_KEY=%s\n' "$DNS_ADMIN_KEY"; } > "$ENVFILE"
  cp "$(cd "$(dirname "$0")" && pwd)/acme-dns.sh" "$SAIDA/acme-dns.sh"
  chmod +x "$SAIDA/acme-dns.sh"
  PROVIDER=exec
else
  aws configure export-credentials --format env-no-export > "$ENVFILE" 2>/dev/null \
    || erro "nao consegui exportar a credencial (precisa de aws cli v2). Tente no CloudShell."
  { printf 'AWS_REGION=%s\n' "${AWS_REGION:-us-east-1}"
    printf 'AWS_HOSTED_ZONE_ID=%s\n' "$ZONA_ID"; } >> "$ENVFILE"
  PROVIDER=route53
fi

docker run --rm -i \
  -v "$SAIDA:/data" -e LEGO_PATH=/data \
  --env-file "$ENVFILE" \
  "$LEGO_IMG" run \
    --server "$SERVIDOR" \
    --accept-tos --email "$EMAIL" \
    --dns "$PROVIDER" \
    --dns.propagation.wait 30s \
    --key-type rsa2048 \
    -d "$NOME" \
  || erro "o lego falhou. Veja a mensagem acima."

CERT=$(find "$SAIDA/certificates" -name '*.crt' | head -1)
CHAVE=$(find "$SAIDA/certificates" -name '*.key' | head -1)
[ -f "$CERT" ] && [ -f "$CHAVE" ] || erro "o lego terminou mas nao achei os arquivos."

cp "$CERT"  "$SAIDA/cert.pem"
cp "$CHAVE" "$SAIDA/key.pem"
chmod 600 "$SAIDA/key.pem"

printf '\n'
ok "certificado em $SAIDA/cert.pem"
ok "chave em       $SAIDA/key.pem"
printf '\n  Nomes cobertos:\n'
openssl x509 -in "$SAIDA/cert.pem" -noout -ext subjectAltName | sed 's/^/   /'
printf '  Valido ate: %s\n' "$(openssl x509 -in "$SAIDA/cert.pem" -noout -enddate | cut -d= -f2)"

printf '\n  %sInstalar numa VM:%s\n\n' "$N" "$F"
printf '    scp -i <chave.pem> %s/cert.pem %s/key.pem \\\n' "$SAIDA" "$SAIDA"
printf '        ubuntu@<nome>.%s:~/labproxy/cert/\n' "$ZONA"
printf '    ssh ... "cd ~/labproxy && bash ~/labproxy/proxy.sh"\n\n'
printf '  O proxy.sh detecta a troca pela soma do certificado e reinicia o Caddy.\n\n'
