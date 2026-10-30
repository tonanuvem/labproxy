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

**Em que conta cada zona vive** — isto importa mais do que parece, e custou
uma sessão inteira para ficar claro:

| Zona | Conta | Quem cria |
|---|---|---|
| `tonanuvem.com`, `lab.tonanuvem.com` | pessoal do instrutor (andre-aws) | à mão |
| `aluno.lab.tonanuvem.com` | **turma, AWS Academy** (`voclabs`, `fiapaws4@outlook`) | o projeto `dns` |

A zona pessoal guarda só o registro NS que delega `aluno.lab.tonanuvem.com`
para os nameservers criados na conta da turma.

A consequência prática: **tudo que escreve na zona da turma precisa de
credencial da conta Academy**, e credencial de Academy é de sessão — expira
em poucas horas. Foi o que impediu a emissão do certificado nesta sessão: o
`emitir-cert.sh` precisa criar um TXT em `_acme-challenge.aluno.lab...`, e o
token já tinha caducado. O Lambda do projeto `dns` não tem esse problema,
porque roda com a `LabRole`, que não expira. Ver §6.1.

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
    auto_https disable_redirects   # as aplicacoes sao HTTP de proposito (§3.5)
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
— `tonanuvem.com` — e não por aluno. Conferido na documentação em
2026-10-29: **50 certificados a cada 7 dias** por domínio registrado,
**100 identificadores** por certificado, e **5 por conjunto exato de nomes**
a cada 7 dias, este sem exceção possível.

Delegar uma zona por turma **não multiplica** o limite de 50: o domínio
registrado é calculado pela Public Suffix List, e `turma1.lab.tonanuvem.com`
e `turma2.lab.tonanuvem.com` caem no mesmo balde. Delegar por turma é bom
por isolamento operacional, não por limite. E vale separar dois números que
se confundem: o teto de **100 nomes** limita quantos alunos cabem num
certificado; o de **50 por semana** limita quantas *emissões* você faz, e
não tem relação com o número de alunos. Como a VM é recriada
semanalmente, qualquer desenho com emissão por aluno estoura o balde em
poucas semanas e trava a emissão para a organização inteira.

> **Desatualizado pelo §3.5.** Desde que as aplicações passaram a ser HTTP,
> o certificado cobre apenas o nome do painel, e um
> `*.<turma>.lab.tonanuvem.com` dá conta de todos os alunos. O que segue
> continua valendo como registro de por que não há emissão por aluno.

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

### 3.5 Painel em HTTPS, aplicações em HTTP

Esta decisão veio depois das outras e encolheu o problema, então vale ler
antes do §3.3 e do §3.4 — ela reduz os dois.

A pergunta que a provocou foi: dá para redirecionar de HTTPS para HTTP e
escapar do aviso de certificado? **Não dá, e isso foi medido.** O handshake
TLS acontece antes de qualquer byte de HTTP, então o navegador valida o
certificado antes de poder receber um redirect:

```
(OUT) TLS handshake, Client hello
(IN)  TLS handshake, Server hello
(IN)  TLS handshake, Certificate
      SSL certificate problem: self signed certificate   ← morre aqui
```

Nenhum `> GET` no trace: a requisição nunca foi enviada, então o 301 nunca
existiu. É o propósito do TLS — quem você não autenticou não pode te mandar
embora.

Mas a pergunta levou a outra: **o certificado só precisava do curinga por
aluno porque as portas eram HTTPS.** Com as aplicações em HTTP:

| | |
|---|---|
| `https://joao.aluno.lab.tonanuvem.com` | painel, coberto por `*.aluno.lab.tonanuvem.com` |
| `http://3000.joao.aluno.lab.tonanuvem.com` | aplicação, sem certificado nenhum |

O que isso muda:

- **Um SAN por turma, para sempre.** Acabou o `1 + N`. Aluno novo não exige
  reemissão, o antigo §6.4 some, e a emissão sequencial de N desafios
  DNS-01 vira um desafio só.
- **O nome do aluno sai do Certificate Transparency.** Antes cada
  `*.joao.aluno...` era publicado; agora só o curinga da turma aparece. O
  vetor de descoberta que motivou o §5 enfraquece bastante.
- **O autoassinado passa a ser usável.** Cobre um nome só, então o aluno
  clica no aviso do navegador uma vez, não uma vez por aplicação — que era a
  pior parte de viver sem certificado real.

O preço, e é real:

- As aplicações ficam fora de *secure context*: service worker, área de
  transferência, câmera e afins não funcionam, e o navegador marca campo de
  senha como "Não seguro".
- **Não resolve o §1.** Navegador em HTTPS-Only continua interceptando a
  navegação para `http://`. Isto reduz o problema do certificado ao mínimo;
  não elimina o atrito do HTTPS-Only.
- Com a proteção ligada, a senha viaja em texto claro nas aplicações. Como
  é a senha pública da turma, perde-se pouco — ela existe para afastar
  varredura, não para guardar segredo. O painel diz isso na página.

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
├── emitir-cert.sh         # lado do INSTRUTOR: emite o certificado da turma
├── acme-dns.sh            # cola do lego para a API da turma (--dns exec)
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
plugin de DNS). **Escuta a 443 e a 80.**

A 443 serve só o painel, e é o único lugar com certificado. A 80 serve as
aplicações, pelo roteamento por porta, e entrega todo o resto — o IP cru,
nomes de fora da zona — à página de atalhos do lab.

**Sobre o Apache.** Ele já rodava na VM ocupando a 80, e a primeira versão
disto o movia para o loopback, editando `ports.conf` e os vhosts. Era
remendo demais, e frágil: no Ubuntu é `apache2`, no Amazon Linux é `httpd`,
com caminhos diferentes. Olhando o Ansible que provisiona a VM, ele serve
**um único arquivo estático** — a página de atalhos, baixada de
`tonanuvem.github.io/page/docs/open.html` para `/var/www/html/index.html`
nas duas distribuições. Não há estado nem configuração a preservar.

Então o `proxy.sh` copia essa página, para o Apache com um
`systemctl disable --now`, e o Caddy serve o mesmo arquivo na 80. Quem
chegava pelo IP vê exatamente o que via antes. Uma linha, igual nas duas
distribuições, e desfazer é outra linha.

`auto_https disable_redirects` continua obrigatório, agora por outro
motivo: as aplicações são HTTP **de propósito**, e sem isso o Caddy
redirecionaria todas para HTTPS e desfaria o desenho do §3.5.

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

**Resolvido por basic auth com a senha da turma — mas desligado por
padrão, e ligado pelo aluno no painel.** Era a única das três opções que
não depende de nada externo: o Security Group do AWS Academy é aberto por
desenho, e o `forward_auth` contra a API precisaria de um endpoint que não
existe.

A proteção cobre **as duas** entradas: o painel e todos os
subdomínios-porta. Não é identidade, é "você está nesta aula": a senha da
turma é pública no bundle do frontend. O que ela resolve é o scanner de
Certificate Transparency, que é o vetor real — não um colega curioso.

**A autenticação não mora no Caddyfile, mora no painel.** Se morasse no
Caddyfile, ligar e desligar exigiria o painel reescrevendo a config do
Caddy e recarregando pela admin API. Como o Caddy já consulta o painel a
cada requisição de porta pelo `forward_auth`, sai mais simples o painel
ser a autoridade única — de autenticação e de allowlist. Verificado: num
401 o Caddy devolve a resposta do painel como ela é, então o
`WWW-Authenticate` chega ao navegador e o prompt aparece.

As regras do botão não são simétricas, de propósito:

- **Ligar não exige credencial.** Com o lab aberto, quem cair na página
  pode fechá-la, e o pior que isso faz é deixar o lab mais seguro. Não dá
  para trancar o aluno para fora, porque a senha é sempre a da turma, que
  ele conhece.
- **Desligar exige credencial**, senão a proteção não protegeria nada.

O estado vai para disco, num bind mount que o `proxy.sh` cria gravável —
o painel roda como `nonroot`, e um volume nomeado nasceria de root. Se a
proteção caísse num `docker compose restart`, o lab ficaria aberto sem
ninguém perceber.

`LABPROXY_PROTECAO=on` sobe com ela já ligada.

> **O padrão aberto é uma decisão consciente, e cara.** Com a publicação
> de portas do host (acima), o `code-server` da VM fica alcançável na
> internet num nome que o Certificate Transparency publica. Se ele roda
> com `--auth none`, isso é execução remota de comandos sem senha. O
> painel avisa disso em vermelho, com o botão ao lado. Para uma turma
> avaliativa, subir com `LABPROXY_PROTECAO=on`.

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

**Como ficou.** O aluno não declara nada. O Caddy pergunta ao painel, por
`forward_auth`, se pode proxyar aquela porta, e o painel responde. Uma porta
é liberada se houver algo escutando nela na interface do host **e** ela não
estiver na denylist administrativa.

A primeira versão perguntava isso ao Docker, e estava errada: o `code-server`
do lab **não roda em container**, nem o `npm run dev` que o aluno sobe direto
na VM. Tudo isso dava 403. A pergunta certa não é "o Docker conhece esta
porta?", é "há algo escutando onde o Caddy vai bater?" — então o painel
pergunta do mesmo jeito que o Caddy, abrindo uma conexão TCP para
`host.docker.internal:<porta>`.

Isso preserva de graça a regra do §5: uma porta presa ao `127.0.0.1` não
atende por `host.docker.internal`, o discador falha, e ela continua
inalcançável — sem precisar ler `/proc` nem dar mais privilégio ao painel.

O Docker continua sendo consultado, mas só para **desenhar** a página: é dele
que vêm o nome do container e a imagem. Uma porta de processo do host aparece
marcada como *no host*.

Isso é dinâmico de propósito: um container que sobe depois é alcançável sem
regenerar o Caddyfile nem recarregar o Caddy. Verificado — um container
publicado após a subida do proxy respondeu na hora.

A denylist continua existindo, porque a allowlist por si só não salva: a
Admin API do Kong **é** uma porta publicada por um container. Ela nasce com
22, 2375/2376/2377, 8001, 8444, 9000/9001, e aceita `PORTAS_BLOQUEADAS` para
acrescentar e `PORTAS_LIBERADAS` para abrir uma porta administrativa de
propósito numa aula.

A **22 entrou na denylist junto com a descoberta de processos do host**, e a
ordem importa: enquanto o painel só olhava containers, ela era barrada por
acidente — o `sshd` não é container nenhum. Passando a olhar o host, ela
entraria sozinha na allowlist. Proxyá-la não daria SSH de todo jeito, porque
o `reverse_proxy` fala HTTP e o handshake do SSH não é HTTP (dá 502), mas
deixá-la fora é explícito e não depende desse acidente.

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

### Túnel SSH sobre a 443

**Desligado por padrão.** É uma porta de administração exposta na internet,
e só existe por um motivo concreto: a rede de quem acessa pode bloquear a
saída na 22. Foi medido, contra uma VM do lab:

```
35.168.12.24:22   bloqueada
35.168.12.24:443  aberta
35.168.12.24:80   aberta
```

Quem bloqueia é a rede de quem acessa, não a AWS — o Security Group está
aberto. Antes de chegar aqui, vale descartar o mal-entendido: `ssh` por
`22.joao.<zona>` **não funciona e nunca vai funcionar**, por três motivos
independentes. A 22 está na denylist; o `reverse_proxy` fala HTTP e o
handshake do SSH não é HTTP (dá 502); e, o decisivo, o cliente `ssh` não lê
o cabeçalho `Host` — ele resolve o nome e conecta na 22 por padrão, passando
longe do Caddy. O esquema `<porta>.<nome>` é uma convenção de HTTP.

O que funciona é encapsular SSH em WebSocket, que é HTTP e atravessa o
Caddy. `LABPROXY_TUNEL=on` sobe um `wstunnel` numa porta interna, e o Caddy
expõe um caminho na 443 para ele. Duas travas, ambas verificadas:

| Trava | Tentativa | Resultado |
|---|---|---|
| caminho | prefixo errado | 404, o Caddy nem roteia |
| destino fixo (`--restrict-to`) | tunelar para `1.1.1.1:443` | 400, o wstunnel recusa |
| chave do SSH | é o portão de verdade, inalterado | |

**Não há segredo no caminho por padrão**, e isso é deliberado. O caminho é
`_tunel`, público e curto. Um segredo ali não protegeria nada que a porta 22
direta não exponha — o `sshd` já está na internet pelo Security Group aberto
do lab — e só deixaria o comando mais longo. O portão continua sendo a chave
do SSH. `LABPROXY_TUNEL_SEGREDO` troca `_tunel` por um segredo, para turma
avaliativa ou para o dia em que o Security Group deixar de ser aberto.

O roteamento é por caminho, e não pelo cabeçalho de upgrade, por um motivo
medido: o Caddy serve HTTP/2, e nele `Connection` e `Upgrade` são proibidos
e removidos. Um matcher de cabeçalho funcionava só em HTTP/1.1 — com h2 a
requisição caía no painel.

Quando o túnel está ligado, **o painel mostra o bloco pronto** — antes disso
a configuração só saía no fim do `proxy.sh`, que rola e some.

Do lado de quem acessa, `brew install wstunnel` (ou o binário dos releases)
e um bloco no `~/.ssh/config`:

```
Host lab
    HostName joao.aluno.lab.tonanuvem.com
    User ubuntu
    IdentityFile ~/caminho/labsuser.pem
    ProxyCommand wstunnel client -L stdio://host.docker.internal:22 \
                   -P <segredo> wss://joao.aluno.lab.tonanuvem.com
```

Depois disso é `ssh lab`, e `scp`, `rsync` e `ssh -L` funcionam por cima
dele como de costume: o túnel é transparente no nível TCP, e o `ssh` não
sabe que existe. **A chave `.pem` é usada exatamente como no acesso
direto** — o túnel troca o transporte, não a autenticação.

O `host.docker.internal:22` parece estranho do lado do cliente, mas é o
servidor quem resolve esse destino — e é exatamente o que o `--restrict-to`
autoriza.

Verificado de ponta a ponta com um `sshd` real atrás do túnel, autenticando
por chave e rodando um comando: `OK: ubuntu@d09bdcfd5870 -- cheguei pelo
túnel`. O mesmo `ssh -i` funciona com e sem o `ProxyCommand`.

**Enquanto o certificado for autoassinado, o cliente não valida o
servidor** — o `wstunnel` conecta em qualquer coisa por padrão. Depois do
certificado real, acrescentar `--tls-verify-certificate`. Até lá, o túnel é
protegido só pelo segredo do caminho.

### O Caddy não recarrega sozinho

O `Caddyfile` e os certificados entram no container por bind mount. Mudar
esses arquivos **não muda a definição do serviço**, então `docker compose
up -d` não recria o Caddy e ele segue com a configuração antiga. E o Caddy
também não relê certificado do disco por conta própria — medido: troquei o
arquivo embaixo dele e continuou servindo o serial anterior; `caddy reload`
falha porque a admin API está desligada.

Juntos, os dois davam um sintoma silencioso: ligar o túnel ou trocar o
certificado não tinha efeito nenhum até alguém reiniciar o container à mão.
O `proxy.sh` agora guarda uma soma do `Caddyfile` mais o certificado e
reinicia o Caddy quando ela muda.

### `host.docker.internal` no Linux

Não existe por padrão — é um alias do Docker Desktop. No EC2 Linux exige
`--add-host=host.docker.internal:host-gateway` (Docker 20.10+), ou
`extra_hosts` no compose. Confirmado no teste: resolve para o endereço do
`docker0` (172.17.0.1), e funciona mesmo com o Caddy numa rede bridge
própria do compose.

**Não validar este projeto no Docker Desktop do macOS.** Ele mente de três
formas, e as três já custaram caro:

1. Uma porta publicada só em `127.0.0.1` *é* alcançável pelo proxy lá, o
   oposto do que acontece na EC2. O teste local deu esse falso resultado
   antes de rodar na VM.
2. O alias `host.docker.internal` existe em **todo** container
   automaticamente, sem `extra_hosts`. No Linux ele só existe onde foi
   declarado. O `painel` ficou sem a declaração e a descoberta de portas do
   host nunca funcionou na VM — toda porta de processo fora do Docker era
   negada, em silêncio, enquanto os containers apareciam normalmente. O
   teste local passava, porque lá o alias estava lá de graça.

3. Um container que escreve num bind mount escreve **como root** no Linux, e
   os arquivos ficam ilegíveis para quem chamou. No macOS o Docker Desktop
   mapeia o dono e o problema some. O `emitir-cert.sh` emitiu o certificado
   no CloudShell e depois não conseguiu lê-lo:
   `find: '/home/cloudshell-user/cert-aluno/certificates': Permission denied`.
   Resolvido com `--user "$(id -u):$(id -g)"` no `docker run`.

Regra que sai disso: **todo serviço que fala com o host precisa do
`extra_hosts` explícito, e todo container que escreve num bind mount precisa
de `--user`** — e nada disso se verifica no macOS.

## 6. Em aberto

0. **Um certificado `*.tonanuvem.com` não serve.** Vale registrar porque a
   pergunta reaparece. Dois motivos independentes: o curinga de TLS casa
   **um rótulo só**, então `*.tonanuvem.com` cobre `x.tonanuvem.com` e não
   chega perto de `joao.aluno.lab.tonanuvem.com`; e um certificado emitido
   no ACM **não exporta a chave privada**, que só pode ser anexada a ALB,
   CloudFront ou API Gateway — o Caddy na VM precisa do arquivo. O que serve
   é `*.<turma>.lab.tonanuvem.com` pela Let's Encrypt com DNS-01 (§3.5).
0b. **O desafio DNS-01 deveria sair do `emitir-cert.sh` e ir para o projeto
   `dns`.** Hoje o script escreve o TXT direto no Route 53, o que exige
   credencial da conta Academy na mão de quem emite — e ela expira em horas.
   O Lambda do projeto `dns` já é dono dessa zona, já escreve nela e roda com
   uma role que não expira.

   O desenho: **uma rota na API que já existe**, reusando o `_eh_admin`
   (header `x-admin-key`), o `_enviar_ao_route53` e a configuração da zona —
   nada de serviço novo. `POST /acme/present` e `POST /acme/cleanup`.

   **Não pendurar no `/registros`.** As semânticas de posse são diferentes:
   `/registros` é "um aluno é dono de um alias, provado pelo token", e o TXT
   do ACME não tem dono aluno — é o instrutor agindo sobre a zona. Misturar
   obriga a abrir exceção no `_dono_confere` e no `_validar_entrada`.

   **Do lado do lego, o provider é o `exec`, não o `httpreq`.** Eu tinha
   recomendado `httpreq` por dispensar script de cola; a sessão do repo `dns`
   mostrou que não fecha, e verifiquei: o `httpreq` só emite *basic auth*,
   não dá para mandar `x-admin-key`. Pior, o `lambda_handler` roda
   `verificar_senha(x-api-key)` e devolve 401 **antes** de rotear qualquer
   coisa, e o lego não manda `x-api-key` — então `httpreq` exigiria uma rota
   privilegiada acima do portão de autenticação, no arquivo em que todo o
   resto passa por ele. A cola de ~15 linhas do `exec` é mais barata que essa
   exceção: o script manda os dois headers como qualquer cliente.

   O script recebe exatamente `$1=present|cleanup`, `$2=fqdn`, `$3=value` —
   conferido no `providers/dns/exec/exec.go` do master. `EXEC_MODE` tem de
   ficar **fora do ambiente**: em `RAW` o script recebe o token cru em vez do
   valor pronto, e a comparação é exata contra `"RAW"`, então até `raw`
   minúsculo cai no modo padrão.

   Os limites que tornam isso seguro: só `TXT`, só nomes começando com
   `_acme-challenge.`, só na zona da turma, TTL 60, e **nunca** com a senha
   da turma, que é pública — com ela, qualquer um forjaria validação ACME de
   qualquer nome da zona. Idempotência do `present` sai de graça: o lego não
   lê o corpo da resposta, qualquer 2xx é sucesso.

   **O par `present`/`cleanup` tem de somar e subtrair, não sobrescrever.**
   Se o certificado um dia ganhar o apex `aluno.lab.tonanuvem.com` junto do
   curinga, os dois validam no mesmo `_acme-challenge.aluno.lab...` e
   precisam de dois TXT simultâneos. `present` faz UPSERT da união; `cleanup`
   remove só o próprio valor e apaga o RRset apenas quando esvaziar. Um
   `cleanup` que deleta o RRset inteiro derruba o desafio do outro nome ainda
   em voo.

   **O lado do labproxy já está escrito**, contra o contrato acordado com a
   sessão do repo `dns`: `acme-dns.sh` é a cola que o `lego` chama, e o
   `emitir-cert.sh` escolhe o caminho sozinho — configurado para a API, usa
   `--dns exec` e dispensa credencial AWS; sem isso, segue no `--dns route53`
   de hoje. Nada quebra enquanto a rota não existir.

   Contrato, confirmado dos dois lados: `POST /acme/present` e
   `POST /acme/cleanup`, corpo `{"fqdn", "value"}` nos dois, os dois headers
   de autenticação que a API já usa, 200 em sucesso, e `cleanup` idempotente.
   O `fqdn` vai como o `lego` entrega, com ponto final; a API normaliza.

   Verificado contra um endpoint simulado: caminho, campos e headers chegam
   certos, e o script sai não-zero quando a API recusa ou está fora do ar —
   que é o que faz o `lego` abortar em vez de seguir sem o TXT.

   Isso resolve §6.1 e §6.2 juntos, porque o mesmo serviço que valida o
   token de posse passa a poder guardar e servir o certificado. Encaminhado
   à sessão que cuida do repo `dns`.

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
3. ~~**Emissão do certificado da turma.**~~ **Feito:** `emitir-cert.sh`,
   do lado do instrutor. Pede `*.<turma>.lab.tonanuvem.com` à Let's Encrypt
   por DNS-01 no Route 53, rodando o `lego` em Docker. Usa o staging por
   padrão; `--producao` emite de verdade. Falta rodar com credencial válida:
   as da máquina estavam expiradas quando foi escrito.
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
2. §4 — o Caddy escuta só a 443; a 80 é do Apache. *(Revisto depois pelo
   §3.5: hoje ele escuta as duas, e o Apache saiu.)*
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
`LABPROXY_PROTECAO=on`, `LABPROXY_TUNEL=on` (§5), `PORTAS_BLOQUEADAS`,
`PORTAS_LIBERADAS`, `PORTAS_HOST`.

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

### Validado na VM

Em 2026-10-29, na VM `teste5.aluno.lab.tonanuvem.com` (`fiaplab-1-aluno`,
Ubuntu), com o lab de observabilidade inteiro no ar. Medido de fora, pela
internet:

| | |
|---|---|
| painel em HTTPS | 200 |
| `code-server` na 8099, processo **fora do Docker** | 302 para `/login` |
| aplicação em container, 3000 | 200 |
| porta 22 | 403 |
| Admin API do Kong, 8001 | 403 |
| página de atalhos pelo IP | servida pelo Caddy, idêntica à do Apache |
| túnel SSH na 443, com `.pem` | entra, da rede que bloqueia a 22 |

O `curl | bash` a partir do GitHub é o caminho que foi usado o tempo todo.

Dois defeitos que só apareceram aqui, e nenhum deles apareceria no macOS:

1. O serviço `painel` estava **sem `extra_hosts`**, então a descoberta de
   portas do host nunca funcionou na VM — em silêncio, porque os containers
   continuavam aparecendo. Era o motivo do 403 na 8099.
2. O `docker compose up -d` **não recria o Caddy** quando só o `Caddyfile`
   muda, por ser bind mount. Ligar o túnel não tinha efeito nenhum.

### O que ainda não foi exercitado

- Nenhuma emissão real da **Let's Encrypt**: as credenciais da máquina
  estavam expiradas. Mas o fluxo inteiro foi ensaiado em Docker local contra
  o **Pebble**, a CA de teste da própria Let's Encrypt, e fechou:

  ```
  lego → --dns exec → acme-dns.sh → lambda_handler REAL do repo dns
       → zona → DNS (UDP e TCP) → Pebble valida → certificado emitido
  ```

  O certificado saiu com **um SAN só**, `*.aluno.lab.tonanuvem.com`, cobrindo
  o painel de qualquer aluno e não cobrindo `8080.joao...` — que é o desenho
  do §3.5, não um defeito. A zona ficou limpa depois do `cleanup`. Instalado
  num labproxy local, o `curl` validou **sem `-k`** (`ssl_verify=0`) e o
  painel abriu: é a prova de que o aviso do navegador some.

  E em 2026-10-30 o `emitir-cert.sh` correu **contra o staging da Let's
  Encrypt de verdade**, do CloudShell da conta da turma, pelo caminho
  `route53`: o desafio DNS-01 foi criado no Route 53 real, validado, e o
  certificado emitido. O fluxo está provado ponta a ponta.

  Falta só a emissão em **produção**, que é o mesmo comando com
  `--producao`.
- A **descoberta automática do nome** depende de `GET /registros` da API da
  turma, que passou a devolver **500** no fim de 2026-10-29. Enquanto não
  voltar, o `proxy.sh` precisa de `LABPROXY_NOME=<nome>`, e o `dns.sh` do
  aluno também está afetado. O problema é no repositório `dns`, não aqui.
- O botão de proteção foi exercitado com a pilha inteira em Docker local,
  12 de 12, incluindo o `WWW-Authenticate` atravessando o `forward_auth` e o
  estado sobrevivendo a um `restart`. Em VM, não.
- NiFi, o contraexemplo de §8, não foi testado atrás do proxy.
