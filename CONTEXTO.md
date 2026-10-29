# labproxy — contexto para a sessão nova

Proxy reverso com TLS que roda **dentro da VM de cada aluno**, para expor
as aplicações Docker do lab em HTTPS sem que o aluno configure nada.

Este arquivo é o handoff. Ele descreve o que existe, o que foi decidido e
por quê, e o que continua em aberto. O proxy está implementado e rodando
numa VM do lab: ver §7 para a validação do desenho e §9 para como subir.

---

## 1. O problema

Cada aluno recebe uma VM EC2 (AWS Academy) e roda `docker compose up -d`
com quantos containers quiser, publicando portas arbitrárias. Precisa
acessar essas aplicações por HTTPS, de fora, sem editar código nem
configuração do container.

Hoje o lab é HTTP puro. A porta 443 da VM não tem nada escutando, e
navegadores com HTTPS-Only transformam isso num erro confuso de
"servidor não encontrado".

## 2. O que já existe (repositório `dns`, branch `melhorias-dns`)

Um ambiente Route 53 + Lambda + DynamoDB + React-Admin já em produção.

**Zona da turma:** `aluno.lab.tonanuvem.com`, delegada a partir de
`lab.tonanuvem.com`. O nome da turma é parametrizado (`config.sh aluno aluno`).

**Registro do aluno.** O aluno roda na VM:

```bash
curl -s https://raw.githubusercontent.com/tonanuvem/config/main/dns.sh | bash
```

e informa um nome. Isso cria **dois** registros A apontando para o IP
público da VM:

| Nome | Para quê |
|---|---|
| `joao.aluno.lab.tonanuvem.com` | o nome base |
| `*.joao.aluno.lab.tonanuvem.com` | qualquer subdomínio — **é a base deste projeto** |

O curinga existe porque o wildcard **não** cobre o nome base: `*.joao.<zona>`
casa `app.joao.<zona>` mas não casa `joao.<zona>`. Por isso os dois.

**Posse.** O `dns.sh` gera um token aleatório por aluno e guarda em
`~/.fiap_dns_token`. A API armazena só o SHA-256. Quem tem o token pode
reapontar ou apagar o próprio nome; mais ninguém. O IP muda toda semana
(VM recriada), o nome não. Há também uma senha de instrutor, saída do
`terraform output -raw senha_admin`, que passa por cima da posse.

**API:** `https://api.aluno.lab.tonanuvem.com`, autenticada por uma senha
de turma no header `x-api-key`. Essa senha é pública — está no bundle do
frontend e no `dns.sh`. Ela diz "você é desta turma", nunca "este
registro é seu".

**Importante: este projeto não precisa mudar nada no repo `dns`.**
Os registros de que o proxy precisa já são criados hoje.

## 3. Decisões tomadas

### 3.1 Caddy, não Traefik

A vantagem do Traefik é descoberta por labels do Docker — descartada por
requisito, já que o aluno não configura nada. Sem ela, o Caddy resolve o
roteamento por porta **dentro do próprio arquivo de configuração**:

```caddyfile
{
    auto_https disable_redirects   # a 80 é do Apache que já roda na VM
}

*.joao.aluno.lab.tonanuvem.com {
    tls /etc/caddy/cert.pem /etc/caddy/key.pem

    @porta header_regexp porta Host ^([0-9]{2,5})\.
    handle @porta {
        reverse_proxy host.docker.internal:{re.porta.1}
    }
    handle {
        respond "Subdomínio inválido" 404
    }
}

joao.aluno.lab.tonanuvem.com {
    tls /etc/caddy/cert.pem /etc/caddy/key.pem
    reverse_proxy painel:8000
}
```

No Traefik, `HostRegexp` casa a requisição mas não extrai a captura para
montar o endereço do backend — o destino tem que ser um serviço
declarado. Seria preciso manter um proxy próprio por trás dele.

WebSocket, `Connection: Upgrade`, `X-Forwarded-Proto` e streaming sem
buffer são o comportamento padrão do `reverse_proxy` do Caddy.

> **Validado em 2026-10-29**, numa VM do lab (Ubuntu 24.04, Docker 29.9,
> Caddy 2-alpine), contra os containers que já rodavam na máquina. O
> `header_regexp` com `{re.porta.1}` funciona como escrito acima. Os
> resultados, e o que eles mudaram neste documento, estão em §7.

### 3.2 Esquema de nomes: `<porta>.<aluno>.<zona>`

`https://3000.joao.aluno.lab.tonanuvem.com` → `localhost:3000` da VM do joão.

Chegamos a considerar achatar para `3000-joao.aluno.lab.tonanuvem.com`,
para que um único curinga `*.aluno.lab.tonanuvem.com` cobrisse tudo.
**Isso não funciona:** pela RFC 4592, que o Route 53 segue, o `*` só vale
como rótulo inteiro. `*-joao.aluno...` seria um registro literal com esse
nome, não um curinga, e cada porta exigiria um registro A próprio —
inviável, já que as portas são arbitrárias.

A tensão é real e vale registrar:

| Esquema | Curinga de DNS | Certificado |
|---|---|---|
| `3000.joao.<zona>` | funciona | exige curinga por aluno |
| `3000-joao.<zona>` | **não funciona** | um curinga serviria a todos |

Fica o primeiro, e o certificado se resolve como abaixo.

**Limite herdado, confirmado em teste:** o curinga vale para **um rótulo
só**, e isso vem do certificado, não do roteamento. `app.3000.joao.<zona>`
não completa nem o handshake TLS — `*.joao.<zona>` não o cobre. Um app
que queira subdomínios próprios atrás do proxy não tem como funcionar
neste desenho.

### 3.3 Um certificado multi-SAN por turma, emitido pelo instrutor

O limite de emissão da Let's Encrypt é contado **por domínio registrado**
— `tonanuvem.com` — e não por aluno. São da ordem de 50 certificados por
semana para todas as turmas somadas (confirmar o número atual; a Let's
Encrypt alterou a estrutura de limites em 2025). Como a VM é recriada
semanalmente, qualquer desenho com emissão por aluno estoura o balde em
poucas semanas e trava a emissão para a organização inteira.

Solução: **um certificado só, com um SAN curinga por aluno.**

```
*.aluno.lab.tonanuvem.com        (cobre joao.aluno..., maria.aluno..., …)
*.joao.aluno.lab.tonanuvem.com
*.maria.aluno.lab.tonanuvem.com
…
```

Até 100 nomes por certificado. Uma emissão por turma, feita por você,
com DNS-01 a partir da sua máquina ou do CloudShell, onde a credencial
do Route 53 já existe.

Consequências, todas boas:

- Nenhuma VM de aluno fala com a Let's Encrypt.
- Nenhuma VM de aluno precisa de credencial AWS. Isso importa muito no
  AWS Academy, onde **não é possível criar roles nem policies** — só a
  `LabRole`, que é ampla. A ideia de "IAM restrito apenas ao TXT do
  desafio" não é realizável nesse ambiente.
- O proxy sobe em segundos, sem esperar propagação de DNS.

O preço: a chave privada é a mesma em todas as VMs, então um aluno pode
apresentar-se como o host de um colega. Num lab entre colegas isso é
aceitável; num ambiente avaliativo, não. **Decisão consciente.**

### 3.4 Distribuição do certificado

Em aberto, mas com um caminho preferido: um endpoint novo na API que já
existe, autenticado pelo **token de posse** do aluno (`~/.fiap_dns_token`),
não pela senha da turma — a senha da turma é pública, e serviria a chave
privada para qualquer um. O token prova que aquele aluno é dono de um
nome naquela zona.

O `proxy.sh` busca o certificado na subida. Ver §6 para as alternativas.

O endpoint ainda não existe, mas o `proxy.sh` já fala com ele, então o
contrato está fixado pelo lado do cliente:

```
GET $LABPROXY_CERT_URL
  x-api-key:    <senha da turma>
  x-dns-token:  <conteúdo de ~/.fiap_dns_token>
→ 200 {"cert": "-----BEGIN CERTIFICATE-----\n...", "key": "-----BEGIN ..."}
```

Sem `LABPROXY_CERT_URL` o `proxy.sh` reaproveita um certificado já presente
em `cert/` e, se não houver, gera um autoassinado de 30 dias e avisa. Dá
para rodar o lab inteiro assim; o navegador reclama uma vez.

## 4. O que construir — construído

```
labproxy/
├── docker-compose.yml     # caddy + painel + dockerproxy
├── Caddyfile.tmpl         # template; o Caddyfile gerado não é versionado
├── painel/
│   ├── main.go            # servidor, /_authz, /healthz
│   ├── docker.go          # inventário via docker-socket-proxy
│   ├── portas.go          # allowlist, denylist administrativa, não-web
│   ├── html.go            # as três páginas
│   ├── go.mod             # sem dependências fora da stdlib
│   └── Dockerfile         # build estático -> distroless nonroot
└── proxy.sh               # bootstrap: curl | bash, como o dns.sh
```

O painel não tem dependência fora da stdlib do Go: fala com o Docker pela
API HTTP, sem SDK. A VM do lab nem sempre tem rede boa para baixar
módulos, e menos código de terceiros num serviço exposto à internet é
melhor.

**Caddy** — imagem oficial, sem build custom (não há ACME, logo não há
plugin de DNS). **Escuta só a 443.**

A 80 fica com o Apache que já roda no host da VM do lab, e que não vai
sair de lá. Consequências: não publicar a 80 no compose, e desligar o
redirect automático com `auto_https disable_redirects`, senão o Caddy
tenta montar o vhost de redirecionamento. O navegador com HTTPS-Only vai
direto ao HTTPS, que era o problema do §1; quem digitar `http://` na mão
cai no Apache, não no proxy.

**Painel (Go)** — serviço interno na 8000. Lê o socket do Docker, lista
containers e portas publicadas, e devolve uma página com links
clicáveis. Servido na raiz `https://joao.aluno.lab.tonanuvem.com`.

Note que ele **não é mais um proxy**: o Caddy faz o roteamento por porta.
O painel só desenha a página — e, por `/_authz`, decide quais portas o
Caddy pode proxyar (§5). Menos código próprio exposto à internet.

Ele classifica cada porta em cinco situações, e isso é o que a página
mostra: aberta, **não fala HTTP** (banco ou fila — liberada, mas sem link,
senão o aluno clica e leva um 502 sem explicação), bloqueada por ser
administrativa, publicada só em `127.0.0.1`, e `EXPOSE` sem `-p`. Os
containers do próprio labproxy são omitidos do inventário.

**proxy.sh** — descobre o nome do aluno sozinho, consultando
`GET /registros` da API do DNS e casando com o IP público da VM. O IP muda
toda semana, o nome não, então não há o que perguntar. Só pergunta se a
descoberta falhar.

**proxy.sh** — segue o padrão do `dns.sh`: `curl … | bash`, lê o nome do
aluno, busca o certificado, gera o Caddyfile, sobe o compose. Idempotente.

## 5. Segurança — itens obrigatórios

### O painel é público

Este é o risco grave, acima da allowlist de portas. Sem autenticação,
qualquer um que descubra `joao.aluno.lab.tonanuvem.com` recebe o
inventário do lab do aluno com links clicáveis. Entre os containers
típicos está o `code-server`, que em tutorial roda com `--auth none` —
isto é, execução remota de comandos aberta na internet.

Não é hipotético: toda emissão ACME é publicada em Certificate
Transparency, e varredura desses logs encontra os nomes em minutos.

**Resolvido pela primeira opção: basic auth no Caddy com a senha da
turma**, ligado por padrão. Era a única das três que não depende de nada
externo — o Security Group do AWS Academy é aberto por desenho, e o
`forward_auth` contra a API precisaria de um endpoint que não existe.

O `basic_auth` cobre **as duas** entradas: o painel e todos os
subdomínios-porta. Não é identidade, é "você está nesta aula": a senha da
turma é pública no bundle do frontend. O que ela resolve é o scanner de
Certificate Transparency, que é o vetor real — não um colega curioso.

`LABPROXY_AUTH=off` desliga, para a aula em que o aluno precisa apontar um
webhook externo para a própria aplicação. O `proxy.sh` avisa em amarelo.

### Socket do Docker

O painel precisa do socket para listar containers. Montar
`/var/run/docker.sock` dá root no host — não é escalação (o aluno já é
root na própria VM), mas o container está exposto à internet, e um bug no
Go vira root na VM. Usar `docker-socket-proxy` em modo leitura com apenas
`CONTAINERS=1`.

Feito: o serviço `dockerproxy` monta o socket em `:ro` e expõe só
`/containers/json`; o painel nunca vê o socket. Os dois conversam numa rede
`internal: true`, sem rota para fora, e o painel roda em imagem distroless
como `nonroot`.

### Allowlist de portas

Não é a "camada seguinte": é obrigatória, e o motivo não é o SSH.

No teste de §7, `https://8001.<nome>.<zona>` devolveu os 10 KB de JSON da
**Admin API do Kong** — a API de configuração do gateway, inteira, por
HTTPS. As portas 22 e 5432 deram 502, e é exatamente isso que engana:
elas falham porque **não falam HTTP**, não porque alguém as bloqueou.
Uma denylist de 22/2375/2376 dá a sensação de cobertura e não cobre nada
do que importa.

Inverter a lógica: **allowlist das portas que o aluno declarou**, não
denylist das que nos lembramos. O conjunto de portas administrativas que
falam HTTP num lab não é enumerável de antemão — Kong (8001), Konga,
Keycloak, Elastic, painéis de admin de qualquer um dos stacks.

**Como ficou.** O aluno não declara nada: quem declara é o Docker. O Caddy
pergunta ao painel, por `forward_auth`, se pode proxyar aquela porta, e o
painel responde lendo o inventário dos containers. Uma porta só é liberada
se algum container a publicou na interface do host **e** ela não está na
denylist administrativa.

Isso é dinâmico de propósito: um container que sobe depois é alcançável sem
regenerar o Caddyfile nem recarregar o Caddy. Verificado — um container
publicado após a subida do proxy respondeu na hora.

A denylist continua existindo, porque a allowlist por si só não salva: a
Admin API do Kong **é** uma porta publicada por um container. Ela nasce com
2375/2376/2377, 8001, 8444, 9000/9001, e aceita `PORTAS_BLOQUEADAS` para
acrescentar e `PORTAS_LIBERADAS` para abrir uma porta administrativa de
propósito numa aula.

O `/_authz` é **fail-closed**: se o painel não consegue falar com o Docker,
ele não sabe quais portas são legítimas e nenhuma é liberada (503).
Verificado derrubando o `dockerproxy` — a porta que respondia 200 passou a
503, e voltou a 200 com o serviço de pé.

Ressalva honesta: no lab isso não é exposição **nova**. A 8001 já
respondia direto em `http://<ip>:8001` por causa do Security Group
aberto, que é intencional. O que o proxy muda é a **descoberta** — com o
certificado real emitido, o nome aparece no Certificate Transparency e
não é mais preciso saber o IP.

Efeito colateral **bom**, confirmado em Linux real: por
`host.docker.internal` só se alcança o que está publicado na interface do
host. Um container subido com `-p 127.0.0.1:5432:5432` fica inalcançável
pelo proxy (502) mesmo respondendo 200 em `localhost` na própria VM. Vale
documentar como intencional.

### `host.docker.internal` no Linux

Não existe por padrão — é um alias do Docker Desktop. No EC2 Linux exige
`--add-host=host.docker.internal:host-gateway` (Docker 20.10+), ou
`extra_hosts` no compose. Confirmado no teste: resolve para o endereço do
`docker0` (172.17.0.1), e funciona mesmo com o Caddy numa rede bridge
própria do compose.

**Não validar este projeto no Docker Desktop do macOS.** Lá
`host.docker.internal` passa pela VM do Docker, e uma porta publicada só
em `127.0.0.1` *é* alcançável pelo proxy — o oposto do que acontece na
EC2. O teste local chegou a dar esse falso resultado antes de rodar na
VM.

## 6. Em aberto

1. **Distribuição do certificado.** Continua o item crítico. O lado do
   cliente já está pronto e o contrato está em §3.4; falta o endpoint. O
   `proxy.sh` roda sem ele, com autoassinado, o que serve para desenvolver
   mas não para uma aula. Alternativas: bucket S3 entre contas (as contas
   dos alunos são separadas da conta que hospeda o DNS —
   `fiapaws@outlook` — o que complica), ou assar o certificado numa AMI
   (some o problema, mas vira trabalho a cada renovação de 90 dias).
2. **Renovação.** O certificado vale 90 dias. Quem reemite, quando, e
   como as VMs já em pé pegam o novo. O `proxy.sh` avisa quando o
   certificado que ele tem em mão expira em menos de 7 dias, e um
   `proxy.sh` rodado de novo troca o certificado — mas ninguém hoje dispara
   isso sozinho.
3. **Emissão do certificado da turma.** Não há script para o lado do
   instrutor: o multi-SAN de §3.3 ainda é feito à mão, com DNS-01 a partir
   da sua máquina ou do CloudShell.
4. **Turmas novas.** Adicionar um aluno exige reemitir o certificado da
   turma. Aceitável se a lista for conhecida no início; se o aluno
   escolhe o nome em sala, não é.
5. **Cookies.** Gravados em `joao.aluno.lab.tonanuvem.com`, vazam entre
   todas as portas-subdomínio da mesma VM. Dois apps com sessão podem se
   atropelar. Raro e difícil de diagnosticar.

Resolvidos desde a primeira versão deste documento: o roteamento por porta
(§7), a **autenticação do painel** (§5 — basic auth com a senha da turma) e
a **allowlist de portas** (§5 — dinâmica, via `forward_auth` contra o
painel).

## 7. Primeira tarefa — feita

Validado em 2026-10-29 na VM `fiaplab-2-aluno.aluno.lab.tonanuvem.com`
(Ubuntu 24.04, Docker 29.9), com certificado autoassinado e Caddy só na
443. Em vez de subir apps de mentira, os backends foram **os containers
do lab que já estavam no ar** — e o teste correu também de fora, pela
internet e pelo DNS real, sem `--resolve`.

14 de 14 asserções passaram:

| O que | Resultado |
|---|---|
| `8081.<nome>` → container `blue` | OK |
| `8082.<nome>` → container `green` | OK, página diferente do `blue` |
| `8070.<nome>` → container `login` | OK |
| `31337.<nome>` → `konga` | OK — 5 dígitos, limite de cima do regexp |
| raiz sem porta → painel | OK |
| `abc.`, `8.`, `999999.` | 404, como projetado |
| WebSocket | OK (101) |
| `X-Forwarded-Proto: https` | presente |
| porta publicada só em `127.0.0.1` | inalcançável (502) |
| `app.8081.<nome>` | falha no TLS, como esperado |

**O desenho de §3.1 se sustenta.** O que o teste mudou neste documento:

1. §3.1 — o `header_regexp` deixa de ser hipótese.
2. §4 — o Caddy escuta só a 443; a 80 é do Apache.
3. §5 — a allowlist de portas subiu de "camada seguinte" para
   obrigatória, por causa da Admin API do Kong.
4. §3.2 — o curinga de um rótulo só virou limite documentado.
5. §8 — o `Host` chega inalterado, o que agrava o caso do NiFi.

O script do teste e o `Caddyfile` mínimo ficaram na VM, em
`~/teste-na-vm.sh` e `~/labproxy-teste/`. Não foram versionados aqui
porque embutem o nome de um aluno e um certificado autoassinado; quando o
`proxy.sh` existir, ele gera ambos.

### Depois dela

O roteamento de pé liberou a implementação, que está em §4 e §9. A
autenticação do painel e a allowlist de portas foram resolvidas junto (§5).

O caminho crítico que sobra é a **distribuição do certificado** (§6.1):
sem ela o lab roda com autoassinado, e o navegador reclama em toda aula.

## 8. Aplicações que vão exigir configuração

A premissa "qualquer app sem tocar em nada" não vale para tudo. Separar
no material didático:

- **NiFi** valida o header `Host` e devolve `System Error` se o nome não
  estiver em `nifi.web.proxy.host`. Também é sensível a
  `X-ProxyContextPath`. Foi citado como exemplo de app que funcionaria
  sem ajuste — é justamente o contraexemplo.

  O teste de §7 confirmou **por que** isso dói: o Caddy repassa o `Host`
  inalterado, então o backend vê `8080.joao.aluno.lab.tonanuvem.com`, com
  a porta no nome. Como o NiFi não aceita curinga em
  `nifi.web.proxy.host`, seria preciso **uma entrada por porta** — e a
  porta muda quando o aluno mexe no compose. Para apps assim, o caminho é
  o aluno configurar, ou o Caddy reescrever o `Host` (`header_up Host`),
  o que quebra outros apps. Decidir caso a caso no material didático.
- Apps que montam URL absoluta precisam honrar `X-Forwarded-Proto`, ou
  emitem `http://` dentro de página HTTPS e quebram por mixed content. O
  proxy manda o header certo (confirmado em §7); honrá-lo é com o app.
- Só aparece container com porta **publicada** (`-p`). `EXPOSE` sozinho
  não basta — é a confusão mais comum de aluno, e merece aviso no painel.

## 9. Como subir

Na VM do aluno, uma linha:

```bash
curl -s https://raw.githubusercontent.com/tonanuvem/labproxy/main/proxy.sh | bash
```

Pré-requisito: ter rodado o `dns.sh` antes, porque é dele que vem o nome e o
token de posse. O `proxy.sh` descobre o nome sozinho pelo IP da VM, busca o
certificado, gera o Caddyfile, valida com `caddy validate` e sobe. É
idempotente: rodar de novo reaproveita o certificado e recria só o que mudou.

Com o certificado da turma:

```bash
LABPROXY_CERT_URL=https://api.aluno.lab.tonanuvem.com/certificado \
  bash proxy.sh
```

Variáveis úteis: `LABPROXY_NOME` (pular a descoberta), `LABPROXY_SENHA`,
`LABPROXY_AUTH=off`, `PORTAS_BLOQUEADAS`, `PORTAS_LIBERADAS`.

### O que foi verificado na VM

Rodado em 2026-10-29 em `fiaplab-2-aluno.aluno.lab.tonanuvem.com`, com o lab
de API Gateway inteiro no ar (13 containers), medindo de fora pela internet.
16 de 16 asserções:

| | |
|---|---|
| sem credencial, com credencial, senha errada | 401 / 200 / 401 |
| subdomínio-porta também exige credencial | 401 |
| `8081`, `8082`, `9099` publicadas por container | 200 |
| container que subiu **depois** do proxy | liberado sem regenerar nada |
| `8001` e `8444`, Admin API do Kong | 403 |
| `22`, que não é container | 403 |
| `9999`, que ninguém publicou | 403 |
| `443`, o próprio proxy | 403 |
| WebSocket | 101 |
| `abc.<host>` | 302 para o painel |
| `X-Forwarded-Proto`, `X-Labproxy-Porta` | chegam ao backend |
| `dockerproxy` derrubado | tudo 503, e 200 de volta ao restaurar |

O Apache do host seguiu respondendo na 80 durante todo o teste, e nenhum dos
13 containers do lab foi tocado.

### O que ainda não foi exercitado

- Nenhuma emissão real da Let's Encrypt: todo o teste correu com
  autoassinado. O caminho do `LABPROXY_CERT_URL` tem o cliente escrito, mas
  nunca falou com um endpoint de verdade (§6.1).
- Nenhum aluno de verdade rodou o `curl | bash` a partir do GitHub — os
  testes copiaram os arquivos para a VM. O caminho de download do `proxy.sh`
  só será exercitado quando o repositório estiver publicado.
- NiFi, o contraexemplo de §8, não foi testado atrás do proxy.
