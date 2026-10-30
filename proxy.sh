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
#   LABPROXY_TUNEL=on    abre um tunel SSH sobre a 443, para quando a rede de
#                        quem acessa bloqueia a porta 22. Desligado por padrao
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
# Padrao, e nao vazio: o aluno nao deve precisar passar nada para ter HTTPS
# valido. Enquanto a rota nao existir, a busca falha em silencio e o fluxo
# cai no autoassinado, como antes. LABPROXY_CERT_URL=off desliga a tentativa.
CERT_URL="${LABPROXY_CERT_URL:-$API_URL/certificado}"
SENHA="${LABPROXY_SENHA:-$SENHA_API}"
USUARIO="${LABPROXY_USUARIO:-aluno}"
# Desligada por padrao: o lab sobe aberto e o aluno protege pelo painel se
# quiser. A senha vai junto de qualquer jeito, senao nao havera o que ligar.
PROTECAO="${LABPROXY_PROTECAO:-off}"
CADDY_IMG="${LABPROXY_CADDY_IMG:-caddy:2.10-alpine}"
# De onde vem a pagina que o apache da VM servia. O Caddy passa a servi-la.
PAGINA_APACHE="${LABPROXY_PAGINA:-/var/www/html/index.html}"
TUNEL="${LABPROXY_TUNEL:-off}"

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

# A impressao que a API devolveu junto com o certificado, guardada verbatim.
#
# Nao recalculamos. Recalcular exigiria acertar a normalizacao do outro lado,
# e nao acerta: medido, o mesmo PEM da hashes diferentes com e sem a quebra
# de linha final, e quem escreve o arquivo repoe justamente a quebra que a
# API removeu antes de hashear. O 304 morreria em silencio e a VM rebaixaria
# o certificado a cada subida, sem ninguem notar.
#
# Guardando o que a API disse, a comparacao passa a ser entre o valor dela e
# ele mesmo, e a classe inteira de erro de normalizacao desaparece.
impressao_local() {
  [ -s "$DIR/cert/impressao" ] || return 1
  tr -d '[:space:]' < "$DIR/cert/impressao"
}

buscar_cert_api() {
  [ -n "$CERT_URL" ] || return 1
  [ -f "$TOKEN_FILE" ] || { aviso "sem $TOKEN_FILE: rode o dns.sh antes."; return 1; }
  command -v python3 >/dev/null || return 1

  local token url imp codigo resp
  token=$(tr -d '[:space:]' < "$TOKEN_FILE")
  url="$CERT_URL"
  imp=$(impressao_local) && [ -n "$imp" ] && url="$CERT_URL?impressao=$imp"

  # Sem -f: precisamos ver o 304, que nao e' erro e vem com corpo vazio.
  resp=$(curl -sS --max-time 30 -w '\n%{http_code}' \
         -H "x-api-key: $SENHA_API" -H "x-dns-token: $token" "$url" 2>/dev/null) \
    || return 1
  codigo=$(printf '%s' "$resp" | tail -n 1)
  resp=$(printf '%s' "$resp" | sed '$d')

  case "$codigo" in
    304) ok "certificado da turma inalterado"; return 2 ;;
    200) ;;
    403) aviso "a API recusou o token de posse; rode o dns.sh de novo."; return 1 ;;
    404) return 1 ;;   # nada publicado, ou rota ainda nao existe
    *)   return 1 ;;
  esac

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
imp = (o.get("impressao") or "").strip()
if imp:
    open(d + "/cert/impressao", "w").write(imp)
' || return 1
  chmod 600 "$DIR/cert/key.pem"
  return 0
}

# A busca grava direto em cert/, entao guardamos o que havia para poder
# voltar atras se o que vier nao servir.
guardar_cert_atual() {
  rm -rf "$DIR/cert.bak"
  [ -f "$DIR/cert/cert.pem" ] && cp -a "$DIR/cert" "$DIR/cert.bak" 2>/dev/null
  return 0
}

restaurar_cert() {
  [ -d "$DIR/cert.bak" ] || return 0
  cp -a "$DIR/cert.bak/." "$DIR/cert/" 2>/dev/null
  rm -rf "$DIR/cert.bak"
}

descartar_backup() { rm -rf "$DIR/cert.bak"; }

gerar_autoassinado() {
  # A impressao antiga tem de sair junto. Deixa-la para tras faria a VM
  # alegar ter um certificado que nao tem: a API responderia 304 e o
  # autoassinado ficaria para sempre.
  rm -f "$DIR/cert/impressao"

  # Um nome so: o do painel. O aluno passa pelo aviso do navegador uma vez,
  # nao uma vez por aplicacao como acontecia quando as portas eram HTTPS.
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 \
    -keyout "$DIR/cert/key.pem" -out "$DIR/cert/cert.pem" \
    -subj "/CN=$HOST" -addext "subjectAltName=DNS:$HOST" >/dev/null 2>&1 \
    || erro "openssl falhou ao gerar o certificado."
  chmod 600 "$DIR/cert/key.pem"
}

obter_cert() {
  local tinha_bom=0
  [ -f "$DIR/cert/cert.pem" ] && [ -f "$DIR/cert/key.pem" ] && san_cobre "$HOST" \
    && tinha_bom=1

  # 1) certificado da turma, pelo endpoint autenticado por token (§3.4).
  #    A busca grava por cima, entao so aceitamos se o que veio cobrir este
  #    nome: sem esta checagem, uma resposta errada da API trocaria um
  #    certificado bom por um inutil e o painel pararia de abrir.
  local r=1
  if [ "$CERT_URL" != "off" ]; then
    guardar_cert_atual
    buscar_cert_api && r=0 || r=$?
  fi

  # r=2 e' o 304: nada foi gravado, o que esta no disco continua valendo.
  if [ "$r" = "2" ]; then
    descartar_backup
  elif [ "$r" = "0" ]; then
    if san_cobre "$HOST"; then
      ok "certificado da turma obtido de $CERT_URL"
      descartar_backup
    else
      restaurar_cert
      aviso "a API devolveu um certificado que nao cobre $HOST; ignorado."
      [ "$tinha_bom" = "1" ] && ok "certificado anterior mantido"
    fi
  else
    descartar_backup
  fi

  if san_cobre "$HOST" 2>/dev/null; then
    [ "$tinha_bom" = "1" ] && ok "certificado reaproveitado de $DIR/cert/"
  else
    # 3) autoassinado: so para validar o lab, o navegador vai reclamar
    gerar_autoassinado
    aviso "usando certificado ${N}autoassinado${F} — o navegador vai avisar."
    info "  O certificado da turma vem de $CERT_URL quando a rota existir."
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

# --------------------------------------------------------------------- tunel
# SSH por cima da 443, para a rede que bloqueia a 22.
#
# A trava e' o --restrict-to do wstunnel: o tunel so alcanca o SSH do host.
# Nao ha caminho secreto por padrao, e isso e' deliberado — o sshd ja esta
# exposto na internet pelo Security Group aberto do lab, entao um segredo
# aqui nao protegeria nada que a porta 22 direta nao exponha, e so deixaria
# o comando do aluno mais longo. O portao continua sendo a chave do SSH.
#
# O caminho padrao e' publico e curto, "_tunel", so para o Caddy saber o que
# mandar ao wstunnel. Roteamos por caminho, e nao pelo cabecalho de upgrade,
# porque o Caddy serve HTTP/2 e nele Connection/Upgrade sao proibidos e
# removidos — um matcher de cabecalho so funcionaria em HTTP/1.1.
#
# LABPROXY_TUNEL_SEGREDO troca esse caminho por um segredo, para turma
# avaliativa ou para o dia em que o Security Group deixar de ser aberto.
CAMINHO_TUNEL="${LABPROXY_TUNEL_SEGREDO:-_tunel}"

# ----------------------------------------------------------------- Caddyfile
gerar_caddyfile() {
  # A autenticacao nao vive mais aqui: e' do painel (ver §5). O template so
  # precisa do nome do aluno.
  local bloco=""
  if [ "$TUNEL" = "on" ]; then
    bloco=$(printf 'handle /%s/* {\n\t\treverse_proxy tunel:8080\n\t}' "$CAMINHO_TUNEL")
  fi

  TUNEL_BLOCO="$bloco" HOST="$HOST" python3 -I -c '
import os
tmpl = open(os.environ["DIR"] + "/Caddyfile.tmpl").read()
bloco = os.environ["TUNEL_BLOCO"]
saida = []
for linha in tmpl.splitlines():
    if linha.strip() == "#__TUNEL__":
        if bloco:
            saida.append("\t" + bloco)
        continue
    saida.append(linha)
texto = "\n".join(saida).replace("__HOST__", os.environ["HOST"]) + "\n"
open(os.environ["DIR"] + "/Caddyfile", "w").write(texto)
' || erro "falha ao gerar o Caddyfile."

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

  local perfis="" caminho=""
  if [ "$TUNEL" = "on" ]; then
    perfis="tunel"
    caminho="$CAMINHO_TUNEL"
  fi

  cat > "$DIR/.env" <<EOF
COMPOSE_PROFILES=$perfis
LABPROXY_TUNEL_CAMINHO=$caminho
LABPROXY_USUARIO_SSH=$(id -un)
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
  # O Caddyfile e os certificados entram no container por bind mount. Mudar
  # esses arquivos nao muda a definicao do servico, entao o 'up -d' sozinho
  # deixa o Caddy rodando com a config antiga — e o Caddy tambem nao rele
  # certificado do disco por conta propria. Sem este restart, ligar o tunel
  # ou trocar o certificado nao tem efeito nenhum ate alguem reiniciar a mao.
  local soma_antes="" soma_agora
  [ -f "$DIR/.soma_caddy" ] && soma_antes=$(cat "$DIR/.soma_caddy")
  soma_agora=$(cat "$DIR/Caddyfile" "$DIR/cert/cert.pem" 2>/dev/null \
    | { sha256sum 2>/dev/null || shasum -a 256; } | cut -d' ' -f1)

  ( cd "$DIR" && docker compose up -d --build ) \
    || erro "o 'docker compose up' falhou. Veja: cd $DIR && docker compose logs"

  if [ "$soma_antes" != "$soma_agora" ]; then
    ( cd "$DIR" && docker compose restart caddy >/dev/null 2>&1 ) \
      && ok "Caddy reiniciado: a config ou o certificado mudaram"
  fi
  printf '%s\n' "$soma_agora" > "$DIR/.soma_caddy"
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
if [ "$TUNEL" = "on" ]; then
  msg
  msg "  ${N}Tunel SSH sobre a 443${F} (para rede que bloqueia a 22)."
  msg "  No ~/.ssh/config de quem acessa, com 'brew install wstunnel':"
  msg
  msg "    Host ${NOME}"
  msg "        HostName $HOST"
  msg "        User $(id -un)"
  msg "        IdentityFile ~/caminho/sua-chave.pem"
  msg "        ProxyCommand wstunnel client -L stdio://host.docker.internal:22 -P $CAMINHO_TUNEL wss://%h"
  msg
  info "  Depois disso: ssh ${NOME}. A chave .pem e' usada igual ao acesso direto."
  info "  Com certificado autoassinado o cliente nao valida o servidor; depois do"
  info "  certificado real, acrescente --tls-verify-certificate."
fi
msg
msg "  Parar:      cd $DIR && docker compose down"
msg "  Logs:       cd $DIR && docker compose logs -f caddy"
msg "  Apache:     parado; o Caddy serve a mesma pagina na 80."
msg "              Desfazer: docker compose down && sudo systemctl enable --now apache2" 
msg
