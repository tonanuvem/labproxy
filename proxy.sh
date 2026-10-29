#!/usr/bin/env bash
#
# labproxy — proxy reverso com TLS dentro da VM do aluno.
#
#   curl -s https://raw.githubusercontent.com/tonanuvem/labproxy/main/proxy.sh | bash
#
# Idempotente: rodar de novo reconfigura e sobe de novo, sem derrubar nada
# que nao seja do labproxy. Ver CONTEXTO.md.
#
# Variaveis de ambiente, todas opcionais:
#   LABPROXY_NOME        nome do aluno; se vazio, descobre pela API do DNS
#   LABPROXY_CERT_URL    endpoint do certificado da turma (§3.4). Deve devolver
#                        JSON {"cert":"-----BEGIN...","key":"-----BEGIN..."},
#                        autenticado pelo token de posse em ~/.fiap_dns_token
#   LABPROXY_SENHA       senha da protecao (padrao: a senha da turma)
#   LABPROXY_PROTECAO=on sobe com a protecao ja ligada (padrao: desligada;
#                        o aluno liga e desliga pelo painel)
#   PORTAS_BLOQUEADAS    portas administrativas extras: "9200,15672"
#   PORTAS_LIBERADAS     tira da denylist padrao: "8001"
#
set -euo pipefail

DIR="${LABPROXY_DIR:-$HOME/labproxy}"
RAW="${LABPROXY_RAW:-https://raw.githubusercontent.com/tonanuvem/labproxy/main}"
ZONA="${DNS_ZONA:-aluno.lab.tonanuvem.com}"
API_URL="${DNS_API_URL:-https://api.aluno.lab.tonanuvem.com}"
SENHA_API="${DNS_API_KEY:-aluno}"
TOKEN_FILE="${DNS_TOKEN_FILE:-$HOME/.fiap_dns_token}"
CERT_URL="${LABPROXY_CERT_URL:-}"
SENHA="${LABPROXY_SENHA:-$SENHA_API}"
USUARIO="${LABPROXY_USUARIO:-aluno}"
# Desligada por padrao: o lab sobe aberto e o aluno protege pelo painel se
# quiser. A senha vai junto de qualquer jeito, senao nao havera o que ligar.
PROTECAO="${LABPROXY_PROTECAO:-off}"
CADDY_IMG="${LABPROXY_CADDY_IMG:-caddy:2.10-alpine}"
# De onde vem a pagina que o apache da VM servia. O Caddy passa a servi-la.
PAGINA_APACHE="${LABPROXY_PAGINA:-/var/www/html/index.html}"

# Tudo que o proxy.sh precisa buscar quando roda por 'curl | bash'.
# Ao acrescentar um arquivo ao repositorio, acrescente aqui tambem.
ARQUIVOS="docker-compose.yml Caddyfile.tmpl
painel/go.mod painel/Dockerfile
painel/main.go painel/docker.go painel/portas.go painel/host.go
painel/estado.go painel/auth.go painel/html.go"

N=$'\033[1m'; F=$'\033[0m'; V=$'\033[32m'; A=$'\033[33m'; R=$'\033[31m'
msg()  { printf '%s\n' "$*"; }
info() { printf '  %s\n' "$*"; }
ok()   { printf '  %s✓%s %s\n' "$V" "$F" "$*"; }
aviso(){ printf '  %s!%s %s\n' "$A" "$F" "$*"; }
erro() { printf '  %s✗%s %s\n' "$R" "$F" "$*" >&2; exit 1; }

# --------------------------------------------------------------- pre-requisitos
checar_ambiente() {
  command -v docker >/dev/null || erro "docker nao encontrado nesta VM."
  docker compose version >/dev/null 2>&1 || erro "'docker compose' nao disponivel."
  docker info >/dev/null 2>&1 || erro "o daemon do Docker nao responde. Tente: sudo systemctl start docker"
  command -v openssl >/dev/null || erro "openssl nao encontrado."
  command -v curl >/dev/null || erro "curl nao encontrado."
}

# ----------------------------------------------------------- nome do aluno
# O IP da VM muda toda semana, o nome nao. A API do DNS sabe qual nome aponta
# para este IP, entao nao precisamos perguntar nada ao aluno.
descobrir_nome() {
  local ip json
  ip=$(curl -fsS --max-time 10 https://checkip.amazonaws.com 2>/dev/null | tr -d '[:space:]') || return 1
  [ -n "$ip" ] || return 1
  json=$(curl -fsS --max-time 15 -H "x-api-key: $SENHA_API" "$API_URL/registros" 2>/dev/null) || return 1
  command -v python3 >/dev/null || return 1
  printf '%s' "$json" | python3 -I -c '
import json, sys
ip = sys.argv[1]
try:
    regs = json.load(sys.stdin)
except Exception:
    sys.exit(1)
for r in regs:
    if r.get("endereco_ip") == ip and r.get("alias"):
        print(r["alias"]); break
' "$ip" 2>/dev/null
}

resolver_nome() {
  if [ -n "${LABPROXY_NOME:-}" ]; then
    NOME="$LABPROXY_NOME"; info "nome informado: ${N}${NOME}${F}"; return
  fi
  NOME=$(descobrir_nome || true)
  if [ -n "$NOME" ]; then
    ok "nome descoberto pela API do DNS: ${N}${NOME}${F}"; return
  fi
  aviso "nao foi possivel descobrir o nome pelo IP desta VM."
  if [ ! -t 0 ]; then
    erro "rode o dns.sh primeiro, ou passe o nome: LABPROXY_NOME=seunome bash proxy.sh"
  fi
  printf '  Qual o seu nome no DNS (o que voce usou no dns.sh)? '
  read -r NOME
  [ -n "$NOME" ] || erro "nome vazio."
}

# ------------------------------------------------------------------- arquivos
# Quando o script roda por 'curl | bash' nao ha arquivos ao lado dele.
baixar_arquivos() {
  local origem="${BASH_SOURCE[0]:-}" base=""
  if [ -n "$origem" ] && [ -f "$origem" ]; then
    base=$(cd "$(dirname "$origem")" && pwd)
  fi
  mkdir -p "$DIR/painel" "$DIR/cert"

  local f
  for f in $ARQUIVOS; do
    if [ -n "$base" ] && [ -f "$base/$f" ]; then
      cp "$base/$f" "$DIR/$f"
    else
      curl -fsS --max-time 30 "$RAW/$f" -o "$DIR/$f" \
        || erro "falha ao baixar $f de $RAW"
    fi
    [ -s "$DIR/$f" ] || erro "$f veio vazio de $RAW"
  done

  # O painel e' um pacote Go: faltando um .go ele nao compila. Conferir aqui
  # da' um erro legivel, em vez de um stack trace do compilador la' na frente.
  local n
  n=$(ls "$DIR"/painel/*.go 2>/dev/null | wc -l | tr -d ' ')
  [ "$n" -ge 7 ] || erro "so $n arquivos .go no painel; esperava 7. Lista desatualizada?"
  ok "arquivos em ${DIR}"
}

# --------------------------------------------------------------- certificado
# §3.3: um certificado multi-SAN por turma, emitido pelo instrutor. Nenhuma VM
# de aluno fala com a Let's Encrypt nem precisa de credencial AWS.
# Desde que as aplicacoes passaram a ser HTTP, o certificado so precisa
# cobrir o nome do painel. Serve o nome exato, ou o curinga da turma — que e'
# um SAN so para todos os alunos. Ver §3.5.
san_cobre() {
  local host="$1" pai="${1#*.}" sans
  sans=$(openssl x509 -in "$DIR/cert/cert.pem" -noout -ext subjectAltName 2>/dev/null) || return 1
  printf '%s' "$sans" | grep -qF "DNS:$host" && return 0
  printf '%s' "$sans" | grep -qF "DNS:*.$pai"
}

buscar_cert_api() {
  [ -n "$CERT_URL" ] || return 1
  [ -f "$TOKEN_FILE" ] || { aviso "sem $TOKEN_FILE: rode o dns.sh antes."; return 1; }
  command -v python3 >/dev/null || return 1
  local token resp
  token=$(tr -d '[:space:]' < "$TOKEN_FILE")
  resp=$(curl -fsS --max-time 30 -H "x-api-key: $SENHA_API" -H "x-dns-token: $token" \
         "$CERT_URL" 2>/dev/null) || return 1
  printf '%s' "$resp" | python3 -I -c '
import json, sys, os
d = os.environ["DIR"]
try:
    o = json.load(sys.stdin)
    c, k = o["cert"], o["key"]
except Exception:
    sys.exit(1)
if "BEGIN CERTIFICATE" not in c or "BEGIN" not in k:
    sys.exit(1)
open(d + "/cert/cert.pem", "w").write(c)
open(d + "/cert/key.pem", "w").write(k)
' || return 1
  chmod 600 "$DIR/cert/key.pem"
  return 0
}

gerar_autoassinado() {
  # Um nome so: o do painel. O aluno passa pelo aviso do navegador uma vez,
  # nao uma vez por aplicacao como acontecia quando as portas eram HTTPS.
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
    -keyout "$DIR/cert/key.pem" -out "$DIR/cert/cert.pem" \
    -subj "/CN=$HOST" -addext "subjectAltName=DNS:$HOST" >/dev/null 2>&1 \
    || erro "openssl falhou ao gerar o certificado."
  chmod 600 "$DIR/cert/key.pem"
}

obter_cert() {
  # 1) certificado da turma, pelo endpoint autenticado por token (§3.4)
  if [ -n "$CERT_URL" ] && buscar_cert_api; then
    ok "certificado da turma obtido de $CERT_URL"
  # 2) certificado ja presente e valido para este nome
  elif [ -f "$DIR/cert/cert.pem" ] && [ -f "$DIR/cert/key.pem" ] && san_cobre "$HOST"; then
    ok "certificado reaproveitado de $DIR/cert/"
  # 3) autoassinado: so para validar o lab, o navegador vai reclamar
  else
    gerar_autoassinado
    aviso "usando certificado ${N}autoassinado${F} — o navegador vai avisar."
    info "  Para o certificado da turma: LABPROXY_CERT_URL=... bash proxy.sh"
  fi

  openssl x509 -in "$DIR/cert/cert.pem" -noout -checkend 604800 >/dev/null 2>&1 \
    || aviso "o certificado expira em menos de 7 dias."
  san_cobre "$HOST" \
    || aviso "o certificado nao cobre $HOST — o painel vai dar erro no navegador."
}

# ------------------------------------------------------------------ porta 80
# O Caddy precisa da 80 para servir as aplicacoes em HTTP.
#
# O que havia la' era um apache servindo UM arquivo estatico: a pagina de
# atalhos do lab, baixada na provisao. Nao ha estado nem configuracao a
# preservar, entao em vez de remendar a config do apache — que e' apache2 no
# Ubuntu e httpd no Amazon Linux, com caminhos diferentes — o Caddy assume a
# porta e serve a mesma pagina. Uma linha de systemctl, revertivel, igual nas
# duas distribuicoes.

parar_apache() {
  local unidade parou=""
  for unidade in apache2 httpd; do
    if systemctl is-active --quiet "$unidade" 2>/dev/null; then
      sudo systemctl disable --now "$unidade" >/dev/null 2>&1 \
        && parou="$unidade"
    fi
  done
  [ -n "$parou" ] && ok "$parou parado; o Caddy assume a 80 e serve a mesma pagina"
  return 0
}

capturar_pagina() {
  mkdir -p "$DIR/publico"
  if [ -f "$PAGINA_APACHE" ]; then
    cp "$PAGINA_APACHE" "$DIR/publico/index.html" 2>/dev/null \
      && ok "pagina de atalhos preservada de $PAGINA_APACHE"
  fi
  # Sem a pagina original, manda quem chegar pelo IP para o painel.
  if [ ! -f "$DIR/publico/index.html" ]; then
    cat > "$DIR/publico/index.html" <<HTML
<!doctype html><html lang="pt-BR"><meta charset="utf-8">
<title>Lab</title>
<meta http-equiv="refresh" content="0; url=https://$HOST/">
<p>Indo para <a href="https://$HOST/">$HOST</a>.</p>
</html>
HTML
    info "sem $PAGINA_APACHE: a 80 vai mandar para o painel"
  fi
}

liberar_80() {
  capturar_pagina
  parar_apache
  local quem
  quem=$(sudo -n ss -ltnp 2>/dev/null | awk '$4 ~ /(^|:)80$/ {print $6}' | head -1)
  if [ -n "$quem" ] && ! printf '%s' "$quem" | grep -q docker; then
    erro "a porta 80 continua ocupada por: $quem
       O labproxy precisa dela para servir as aplicacoes em HTTP."
  fi
}

# ----------------------------------------------------------------- Caddyfile
gerar_caddyfile() {
  # A autenticacao nao vive mais aqui: e' do painel (ver §5). O template so
  # precisa do nome do aluno.
  sed -e "s/__HOST__/$HOST/g" \
    "$DIR/Caddyfile.tmpl" > "$DIR/Caddyfile" \
    || erro "falha ao gerar o Caddyfile."

  # O validate carrega os certificados de verdade, entao eles precisam estar
  # montados nos mesmos caminhos que o Caddy vai usar em producao.
  caddy_run() {
    docker run --rm \
      -v "$DIR/Caddyfile:/etc/caddy/Caddyfile" \
      -v "$DIR/cert/cert.pem:/etc/caddy/cert.pem:ro" \
      -v "$DIR/cert/key.pem:/etc/caddy/key.pem:ro" \
      "$CADDY_IMG" "$@"
  }

  caddy_run caddy fmt --overwrite /etc/caddy/Caddyfile >/dev/null 2>&1 || true

  if ! caddy_run caddy validate --config /etc/caddy/Caddyfile >/dev/null 2>&1; then
    caddy_run caddy validate --config /etc/caddy/Caddyfile || true
    erro "o Caddyfile gerado nao passou no 'caddy validate'."
  fi
  ok "Caddyfile gerado e validado"
}

# ------------------------------------------------------------------- subida
checar_443() {
  local quem
  quem=$(sudo -n ss -ltnp 2>/dev/null | awk '$4 ~ /:443$/ {print $6}' | head -1) || true
  if [ -n "$quem" ] && ! printf '%s' "$quem" | grep -q docker; then
    aviso "alguem ja escuta na 443: $quem"
  fi
}

subir() {
  # O painel roda como nonroot e precisa gravar aqui se o aluno ligar a
  # protecao. Um volume nomeado nasceria de root, por isso um diretorio nosso.
  mkdir -p "$DIR/estado" && chmod 0777 "$DIR/estado"

  local inicial=0
  [ "$PROTECAO" = "on" ] && inicial=1

  cat > "$DIR/.env" <<EOF
LABPROXY_HOST=$HOST
LABPROXY_CADDY_IMG=$CADDY_IMG
LABPROXY_USUARIO=$USUARIO
LABPROXY_SENHA=$SENHA
PROTECAO_INICIAL=$inicial
PORTAS_BLOQUEADAS=${PORTAS_BLOQUEADAS:-}
PORTAS_LIBERADAS=${PORTAS_LIBERADAS:-}
PORTAS_HOST=${PORTAS_HOST:-}
EOF
  chmod 600 "$DIR/.env"
  ( cd "$DIR" && docker compose up -d --build ) \
    || erro "o 'docker compose up' falhou. Veja: cd $DIR && docker compose logs"
}

verificar() {
  local i code
  for i in 1 2 3 4 5 6 7 8 9 10; do
    code=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 \
           --resolve "$HOST:443:127.0.0.1" "https://$HOST/" 2>/dev/null) || code=000
    case "$code" in
      200|401) ok "proxy respondendo na 443 (HTTP $code)"; return 0 ;;
      403)     ok "proxy respondendo na 443 (HTTP 403)"; return 0 ;;
    esac
    sleep 2
  done
  aviso "o proxy subiu mas nao respondeu como esperado (ultimo status: $code)."
  info "  Veja: cd $DIR && docker compose logs caddy"
}

# --------------------------------------------------------------------- main
msg
msg "  ${N}labproxy${F} — HTTPS para as aplicacoes do seu lab"
msg
checar_ambiente
resolver_nome
HOST="$NOME.$ZONA"
export DIR HOST
msg
baixar_arquivos
obter_cert
gerar_caddyfile
liberar_80
checar_443
msg
subir
msg
verificar
msg
msg "  ${N}Painel:${F}  https://$HOST/        (HTTPS, com certificado)"
msg "  ${N}Um app:${F}  http://<porta>.$HOST/   (ex: http://8080.$HOST/)"
msg
if [ "$PROTECAO" = "on" ]; then
  msg "  ${N}Protecao:${F} ligada — usuario '$USUARIO', a senha da turma"
else
  aviso "o lab esta ${N}aberto na internet${F}. Qualquer um que descubra o"
  info "  endereco alcanca suas aplicacoes. Para fechar, use o botao no painel."
fi
msg
msg "  Parar:      cd $DIR && docker compose down"
msg "  Logs:       cd $DIR && docker compose logs -f caddy"
msg "  Apache:     parado; o Caddy serve a mesma pagina na 80."
msg "              Desfazer: docker compose down && sudo systemctl enable --now apache2" 
msg
