# labproxy — contexto para a sessão nova

Proxy reverso com TLS que roda **dentro da VM de cada aluno**, para expor
as aplicações Docker do lab em HTTPS sem que o aluno configure nada.

Este arquivo é o handoff. Ele descreve o que já existe, o que foi
decidido e por quê, e o que continua em aberto. Nada aqui foi
implementado ainda.

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

> **Não validado.** O `header_regexp` com `{re.porta.1}` foi escrito a
> partir da documentação, sem teste. É a primeira coisa a verificar
> (ver §7).

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

## 4. O que construir

```
labproxy/
├── docker-compose.yml     # caddy + painel
├── Caddyfile              # gerado a partir do nome do aluno
├── painel/                # serviço Go
│   └── main.go
└── proxy.sh               # bootstrap: curl | bash, como o dns.sh
```

**Caddy** — imagem oficial, sem build custom (não há ACME, logo não há
plugin de DNS). Escuta 80 e 443.

**Painel (Go)** — serviço interno na 8000. Lê o socket do Docker, lista
containers e portas publicadas, e devolve uma página com links
clicáveis. Servido na raiz `https://joao.aluno.lab.tonanuvem.com`.

Note que ele **não é mais um proxy**: o Caddy faz o roteamento por porta.
O painel só desenha a página. Menos código próprio exposto à internet.

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

Mitigar com pelo menos um: basic auth no Caddy com a senha da turma;
Security Group restrito ao IP da sala; ou `forward_auth` contra a API.

### Socket do Docker

O painel precisa do socket para listar containers. Montar
`/var/run/docker.sock` dá root no host — não é escalação (o aluno já é
root na própria VM), mas o container está exposto à internet, e um bug no
Go vira root na VM. Usar `docker-socket-proxy` em modo leitura com apenas
`CONTAINERS=1`.

### Allowlist de portas

Bloquear 22, 2375, 2376 e o que mais for administrativo. O Caddy já
restringe a 2–5 dígitos pelo regexp; a allowlist é a camada seguinte.

Efeito colateral **bom** a documentar como intencional: por
`host.docker.internal` só se alcança o que está publicado na interface do
host. Um container subido com `-p 127.0.0.1:5432:5432` fica inalcançável
de fora.

### `host.docker.internal` no Linux

Não existe por padrão — é um alias do Docker Desktop. No EC2 Linux exige
`--add-host=host.docker.internal:host-gateway` (Docker 20.10+).

## 6. Em aberto

1. **Distribuição do certificado.** O endpoint autenticado por token
   (§3.4) é o caminho preferido, mas não foi desenhado. Alternativas:
   bucket S3 entre contas (as contas dos alunos são separadas da conta
   que hospeda o DNS — `fiapaws@outlook` — o que complica), ou assar o
   certificado numa AMI (some o problema, mas vira trabalho a cada
   renovação de 90 dias).
2. **Renovação.** O certificado vale 90 dias. Quem reemite, quando, e
   como as VMs já em pé pegam o novo.
3. **Autenticação do painel** — qual das três opções de §5.
4. **Turmas novas.** Adicionar um aluno exige reemitir o certificado da
   turma. Aceitável se a lista for conhecida no início; se o aluno
   escolhe o nome em sala, não é.
5. **Cookies.** Gravados em `joao.aluno.lab.tonanuvem.com`, vazam entre
   todas as portas-subdomínio da mesma VM. Dois apps com sessão podem se
   atropelar. Raro e difícil de diagnosticar.

## 7. Primeira tarefa

Validar o `header_regexp` numa VM, antes de construir qualquer coisa em
cima. Dois containers em portas diferentes, um Caddyfile mínimo com
certificado autoassinado, e confirmar:

- `https://3000.<nome>` chega no container da 3000;
- `https://8080.<nome>` chega no outro;
- um WebSocket atravessa;
- a raiz, sem porta, cai no handler do painel.

São dez minutos e definem se o desenho de §3.1 se sustenta.

## 8. Aplicações que vão exigir configuração

A premissa "qualquer app sem tocar em nada" não vale para tudo. Separar
no material didático:

- **NiFi** valida o header `Host` e devolve `System Error` se o nome não
  estiver em `nifi.web.proxy.host`. Também é sensível a
  `X-ProxyContextPath`. Foi citado como exemplo de app que funcionaria
  sem ajuste — é justamente o contraexemplo.
- Apps que montam URL absoluta precisam honrar `X-Forwarded-Proto`, ou
  emitem `http://` dentro de página HTTPS e quebram por mixed content.
- Só aparece container com porta **publicada** (`-p`). `EXPOSE` sozinho
  não basta — é a confusão mais comum de aluno, e merece aviso no painel.
