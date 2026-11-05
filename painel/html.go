package main

import (
	"html/template"
	"io"
	"log"
)

type dadosIndex struct {
	Host        string
	Inv         *inventario
	Protegido   bool
	Persistente bool
	TemSenha    bool
	Usuario     string
	Tunel       string // caminho do tunel SSH; vazio quando desligado
	UsuarioSSH  string
	CertAmb     string
	CertExp     string
}

// CertConfiavel diz se o navegador vai aceitar o certificado sem reclamar.
//
// So "producao" conta. Vazio e' tratado como NAO confiavel de proposito: a
// API devolve vazio para certificado guardado antes do campo existir, e
// supor producao ai esconderia justamente o caso que este aviso existe para
// pegar — uma turma rodando semanas em staging, com o cadeado quebrado
// parecendo o de sempre.
func (d dadosIndex) CertConfiavel() bool { return d.CertAmb == "producao" }

func (d dadosIndex) CertRotulo() string {
	switch d.CertAmb {
	case "staging":
		return "de teste (staging da Let's Encrypt)"
	case "autoassinado":
		return "autoassinado"
	case "", "desconhecido":
		return "de origem desconhecida"
	}
	return d.CertAmb
}

type dadosNegado struct {
	Host   string
	Porta  string
	Motivo string
}

// As aplicacoes sao servidas em HTTP, nao HTTPS: o certificado cobre apenas
// o nome do painel. Ver §3.5 do CONTEXTO.md.
func (e entrada) URL(host string) string {
	return "http://" + e.Endereco(host) + "/"
}

// Endereco e' a URL sem o esquema, para a pagina mostrar o "http://" em
// separado. O aluno precisa ver o esquema: e' ele que explica por que o
// navegador nao desenha cadeado na aplicacao.
func (e entrada) Endereco(host string) string {
	return itoa(e.Porta) + "." + host
}

// Aberta: o proxy libera a porta. Clicavel: vale oferecer um link.
func (e entrada) Aberta() bool {
	return e.Situacao == acessivel || e.Situacao == noHost || e.Situacao == naoWeb
}
func (e entrada) Clicavel() bool  { return e.Situacao == acessivel || e.Situacao == noHost }
func (e entrada) Bloqueada() bool { return e.Situacao == bloqueada }

// Rotulo e' a etiqueta da porta — e so existe para o que foge do normal.
//
// "acessivel" nao ganha etiqueta de proposito. Antes do §3.5 as portas eram
// servidas por TLS e esta linha devolvia "HTTPS"; quando as aplicacoes
// viraram HTTP (commit b458fe8) a etiqueta ficou para tras e passou a
// prometer cadeado em cima de link http://. Pior que nao informar e'
// informar o contrario. Hoje o esquema aparece onde ele e' verdade: no
// endereco impresso em cada cartao.
func (e entrada) Rotulo() string {
	switch e.Situacao {
	case noHost:
		return "no host"
	case naoWeb:
		return "nao fala HTTP"
	case bloqueada:
		return "bloqueada"
	case soLoopback:
		return "so no localhost"
	case soExpose:
		return "sem -p"
	}
	return ""
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [6]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

const base = `<!doctype html>
<html lang="pt-BR"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{template "titulo" .}}</title>
<style>
:root{
  --sobre-acc:#fff;
  --bg:#fbfbfa;--fg:#1a1a18;--dim:#64645f;--dim2:#8c8c86;
  --linha:#e5e5e1;--linha2:#efefec;--card:#fff;
  --ok:#1b7f4b;--bloq:#a4461f;--alerta:#8a6300;--acc:#2a5db0;
  --chip:#edf2fb;--chip-b:#dbe5f6;
  --sombra:0 1px 2px rgba(20,20,20,.05);--sombra-h:0 3px 12px rgba(42,93,176,.13)
}
@media (prefers-color-scheme:dark){:root{
  --bg:#141518;--fg:#e9e9e5;--dim:#9b9b95;--dim2:#787872;
  --linha:#2c2e33;--linha2:#24262a;--card:#1c1e21;
  --ok:#4ac17e;--bloq:#e0875c;--alerta:#d6ab45;--acc:#7fa9f0;
  --chip:#222a38;--chip-b:#2d3647;
  --sombra:none;--sombra-h:0 3px 12px rgba(0,0,0,.35);
  --sobre-acc:#14151a
}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);
  font:15px/1.55 ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,sans-serif;
  -webkit-font-smoothing:antialiased}
.wrap{max-width:940px;margin:0 auto;padding:40px 18px 72px}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}

/* cabecalho ------------------------------------------------------------ */
header.topo{margin:0 0 24px}
h1{font-size:21px;margin:0 0 5px;font-weight:660;letter-spacing:-.015em}
.host{font:13px ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  color:var(--dim);margin:0;word-break:break-all}

/* faixa de estado do lab ----------------------------------------------- */
.estado{display:flex;gap:13px;align-items:flex-start;margin:0 0 30px;padding:14px 16px;
  border:1px solid var(--linha);border-radius:11px;background:var(--card);box-shadow:var(--sombra)}
.estado .ponto{flex:0 0 auto;width:9px;height:9px;border-radius:50%;margin-top:7px;
  background:var(--ok);box-shadow:0 0 0 3px color-mix(in srgb,var(--ok) 18%,transparent)}
.estado.aberto{border-color:color-mix(in srgb,var(--alerta) 45%,var(--linha))}
.estado.aberto .ponto{background:var(--alerta);
  box-shadow:0 0 0 3px color-mix(in srgb,var(--alerta) 18%,transparent)}
.estado .txt{flex:1 1 auto;min-width:0}
.estado h3{margin:0 0 4px;font-size:14px;font-weight:650}
.estado p{margin:0 0 11px;font-size:13.5px;color:var(--dim)}
.estado p:last-child{margin-bottom:0}
.estado form{margin:0}

/* secoes ---------------------------------------------------------------- */
h2{font-size:12px;text-transform:uppercase;letter-spacing:.085em;color:var(--dim);
  font-weight:650;margin:34px 0 4px;display:flex;align-items:center;gap:8px}
h2 .conta{font:600 11px ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:0;
  color:var(--dim);background:var(--linha2);border:1px solid var(--linha);
  border-radius:20px;padding:1px 7px;text-transform:none}
.sub{margin:0 0 12px;font-size:13px;color:var(--dim2)}

/* cartoes das aplicacoes ------------------------------------------------ */
.apps{list-style:none;margin:0;padding:0;display:grid;gap:10px;
  grid-template-columns:repeat(auto-fill,minmax(296px,1fr))}
.card{display:block;height:100%;padding:12px 14px 13px;background:var(--card);
  border:1px solid var(--linha);border-radius:10px;box-shadow:var(--sombra);
  text-decoration:none;color:inherit;transition:border-color .12s,box-shadow .12s}
a.card:hover{border-color:var(--acc);box-shadow:var(--sombra-h)}
a.card:hover .nome{color:var(--acc)}
a.card:focus-visible{outline:2px solid var(--acc);outline-offset:2px}
.card.off{background:transparent;box-shadow:none;border-style:dashed}
.cab{display:flex;align-items:center;gap:9px;margin-bottom:6px}
.porta{flex:0 0 auto;font:650 13px ui-monospace,SFMono-Regular,Menlo,monospace;
  color:var(--acc);background:var(--chip);border:1px solid var(--chip-b);
  border-radius:6px;padding:2px 8px;letter-spacing:-.01em}
.card.off .porta{color:var(--dim);background:transparent;border-color:var(--linha)}
.nome{flex:1 1 auto;min-width:0;font-weight:600;font-size:14.5px;letter-spacing:-.005em;
  overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.card.off .nome{font-weight:550;color:var(--dim)}
.tag{flex:0 0 auto;font-size:10.5px;font-weight:600;letter-spacing:.03em;text-transform:uppercase;
  padding:2px 7px;border-radius:20px;border:1px solid var(--linha);color:var(--dim);white-space:nowrap}
.tag.bloq{color:var(--bloq);border-color:color-mix(in srgb,var(--bloq) 50%,transparent)}
.porta .esq{font-weight:500;opacity:.62}
.img .int{white-space:nowrap}
.img{font:11.5px ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  color:var(--dim2);word-break:break-all;line-height:1.45}
.motivo{margin-top:8px;padding-top:7px;border-top:1px solid var(--linha2);
  font-size:12.5px;color:var(--dim);line-height:1.45}
.vazio{margin:0;padding:22px 16px;text-align:center;border:1px dashed var(--linha);
  border-radius:10px;color:var(--dim);font-size:13.5px}

/* notas, ajuda, rodape -------------------------------------------------- */
.nota{border-left:2px solid var(--linha);padding:3px 0 3px 13px;color:var(--dim);
  font-size:13.5px;margin:14px 0}
.nota.alerta{border-left-color:var(--alerta)}
details.ajuda{margin:28px 0 0;border:1px solid var(--linha);border-radius:10px;background:var(--card)}
details.ajuda+details.ajuda{margin-top:8px}
details.ajuda summary{cursor:pointer;padding:11px 15px;font-size:13.5px;font-weight:550;
  list-style:none;display:flex;gap:9px;align-items:center}
details.ajuda summary::-webkit-details-marker{display:none}
details.ajuda summary::before{content:"\203A";color:var(--dim);font-size:16px;line-height:1;
  transition:transform .12s;display:inline-block}
details.ajuda[open] summary::before{transform:rotate(90deg)}
details.ajuda summary:hover{color:var(--acc)}
details.ajuda .conteudo{padding:0 15px 14px 33px;font-size:13.5px;color:var(--dim)}
details.ajuda .conteudo p{margin:0 0 9px}
details.ajuda .conteudo p:last-child{margin-bottom:0}
code{font:12.5px ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:var(--linha2);
  border:1px solid var(--linha);border-radius:4px;padding:1px 5px}
pre{font:12.5px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:var(--card);
  border:1px solid var(--linha);border-radius:9px;padding:13px 15px;overflow-x:auto;margin:0 0 12px}
footer{margin-top:46px;padding-top:16px;border-top:1px solid var(--linha);
  color:var(--dim2);font-size:12.5px;line-height:1.6}

/* botoes ---------------------------------------------------------------- */
button{font:inherit;font-size:13px;font-weight:600;padding:7px 15px;border-radius:7px;
  border:1px solid var(--linha);background:var(--bg);color:var(--fg);cursor:pointer;
  transition:border-color .12s,background .12s,color .12s}
button:hover{border-color:var(--acc);color:var(--acc)}
button.primario{background:var(--acc);border-color:var(--acc);color:var(--sobre-acc)}
button.primario:hover{opacity:.9;color:var(--sobre-acc)}

@media (max-width:520px){
  .wrap{padding:28px 14px 56px}
  .apps{grid-template-columns:1fr}
}
</style></head><body><div class="wrap">{{template "corpo" .}}</div></body></html>`

const tmplIndex = `
{{define "titulo"}}Lab de {{.Host}}{{end}}

{{define "cartao"}}
  <div class="cab">
    <span class="porta">{{if .E.Clicavel}}<span class="esq">http://</span>{{end}}{{if .E.Porta}}{{.E.Porta}}{{else}}&mdash;{{end}}</span>
    <span class="nome">{{.E.Container}}</span>
    {{if .E.Rotulo}}<span class="tag{{if .E.Bloqueada}} bloq{{end}}">{{.E.Rotulo}}</span>{{end}}
  </div>
  <div class="img">{{.E.Imagem}}{{if .E.PortaInt}}<span class="int"> &middot; container :{{.E.PortaInt}}</span>{{end}}</div>
  {{if .E.Motivo}}<div class="motivo">{{.E.Motivo}}</div>{{end}}
{{end}}

{{define "corpo"}}
{{$host := .Host}}
<header class="topo">
  <h1>Aplicacoes do seu lab</h1>
  <p class="host">{{.Host}}</p>
</header>

{{if .Protegido}}
<div class="estado">
  <span class="ponto"></span>
  <div class="txt">
    <h3>Lab protegido por senha</h3>
    <p>Quem abrir qualquer endereco deste lab precisa entrar com
    <code>{{.Usuario}}</code> e a senha da turma. Nas aplicacoes, que sao HTTP,
    essa senha viaja em texto claro — ela serve para afastar quem varre a
    internet, nao para proteger segredo.</p>
    <form method="post" action="/_protecao">
      <input type="hidden" name="acao" value="desativar">
      <button type="submit">Desligar a protecao</button>
    </form>
  </div>
</div>
{{else}}
<div class="estado aberto">
  <span class="ponto"></span>
  <div class="txt">
    <h3>Lab aberto</h3>
    <p>Quem tiver este endereco abre as aplicacoes abaixo sem senha. Se voce
    roda algo que aceita comandos, como o <code>code-server</code>, convem
    fechar.</p>
    {{if .TemSenha}}
    <form method="post" action="/_protecao">
      <input type="hidden" name="acao" value="ativar">
      <button class="primario" type="submit">Proteger com a senha da turma</button>
    </form>
    {{else}}
    <p>Nao ha senha configurada nesta VM. Suba o proxy de novo com
    <code>LABPROXY_SENHA=...</code> para poder proteger.</p>
    {{end}}
  </div>
</div>
{{end}}

{{if not .Persistente}}
<p class="nota alerta">O painel nao consegue gravar o estado da protecao em disco:
se o container reiniciar, ela volta ao padrao.</p>
{{end}}

<h2>Abertas pelo proxy <span class="conta">{{len .Inv.Abertas}}</span></h2>
{{with .Inv.Abertas}}
<p class="sub">Cada uma responde em
<code>http://&lt;porta&gt;.{{$host}}</code> — sem cadeado: o certificado cobre
so este painel.</p>
<ul class="apps">{{range .}}
  <li>{{if .Clicavel}}
    <a class="card" href="{{.URL $host}}" target="_blank" rel="noopener noreferrer">
      {{template "cartao" (cartao . $host)}}
    </a>
  {{else}}
    <div class="card off">{{template "cartao" (cartao . $host)}}</div>
  {{end}}</li>
{{end}}</ul>
{{else}}
<p class="vazio">Nada escutando ainda. Suba um container com
<code>docker compose up -d</code>, ou um processo direto na VM, e recarregue.</p>
{{end}}

{{with .Inv.NaoExpostas}}
<h2>Nao expostas <span class="conta">{{len .}}</span></h2>
<p class="sub">O proxy nao alcanca estas portas — o cartao diz por que.</p>
<ul class="apps">{{range .}}
  <li><div class="card off">{{template "cartao" (cartao . $host)}}</div></li>
{{end}}</ul>
{{end}}

<details class="ajuda">
  <summary>Meu container nao aparece aqui</summary>
  <div class="conteudo">
    <p>Container so aparece com porta <strong>publicada</strong>
    (<code>-p 3000:3000</code>). <code>EXPOSE</code> sozinho nao basta — e' a
    confusao mais comum.</p>
    <p>Processo rodando direto na VM, fora do Docker, aparece como
    <em>no host</em>. Uma porta presa ao <code>127.0.0.1</code> fica
    inalcancavel de fora de proposito, e por isso nao entra nas abertas.</p>
    <p>Banco e fila (<code>5432</code>, <code>3306</code>, <code>6379</code>…)
    sao liberados, mas sem link: nao falam HTTP, e o clique so renderia um 502
    sem explicacao.</p>
  </div>
</details>

<details class="ajuda">
  <summary>Por que a aplicacao abre em http:// e o painel em https://</summary>
  <div class="conteudo">
    <p>O certificado cobre um nome so, o deste painel. As aplicacoes sao
    servidas em HTTP de proposito: assim um aluno novo nao exige reemissao de
    certificado, e o seu nome nao vai parar no Certificate Transparency.</p>
    <p>O preco e' real: fora de <em>secure context</em> o navegador marca
    campo de senha como "Nao seguro", e recursos como area de transferencia e
    camera nao funcionam na aplicacao.</p>
  </div>
</details>

{{if not .CertConfiavel}}
<p class="nota alerta">O certificado deste painel e' <strong>{{.CertRotulo}}</strong>,
entao o navegador avisa que a conexao nao e' confiavel. O trafego vai
cifrado do mesmo jeito; o que falta e' a assinatura que o navegador
reconhece.{{if .CertExp}} Vence em {{.CertExp}}.{{end}}</p>
{{end}}

{{if .Tunel}}
<h2>Acesso SSH</h2>
<p class="sub">A porta 22 desta VM esta aberta, mas algumas redes bloqueiam a
saida nela. Este tunel leva o SSH por dentro do HTTPS, na 443. Instale o
<code>wstunnel</code> e acrescente ao seu <code>~/.ssh/config</code>:</p>
<pre>Host lab
    HostName {{.Host}}
    User {{.UsuarioSSH}}
    IdentityFile ~/caminho/sua-chave.pem
    ProxyCommand wstunnel client -L stdio://host.docker.internal:22 -P {{.Tunel}} wss://%h</pre>
<p class="nota">Depois disso e' <code>ssh lab</code>. A chave <code>.pem</code>
e' usada igual ao acesso direto — o tunel troca o transporte, nao a
autenticacao.</p>
{{end}}

<footer>labproxy &middot; o painel e' a unica pagina em HTTPS &middot; as
aplicacoes sao servidas em HTTP, por desenho</footer>
{{end}}`

const tmplNegado = `
{{define "titulo"}}Porta {{.Porta}} nao liberada{{end}}
{{define "corpo"}}
<header class="topo">
  <h1>Porta {{.Porta}} nao esta liberada</h1>
  <p class="host">{{.Porta}}.{{.Host}}</p>
</header>
<p class="nota alerta">{{.Motivo}}</p>
<p class="vazio">Veja o que esta no ar em <a href="https://{{.Host}}/">{{.Host}}</a>.</p>
{{end}}`

const tmplLogin = `
{{define "titulo"}}Lab protegido{{end}}
{{define "corpo"}}
<header class="topo">
  <h1>Este lab esta protegido</h1>
  <p class="host">{{.Host}}</p>
</header>
<p class="nota">Entre com o usuario e a senha da turma. Se voce e' o dono desta
VM e nao lembra a senha, ela e' a mesma que o <code>dns.sh</code> usa.</p>
{{end}}`

const tmplErro = `
{{define "titulo"}}Painel sem Docker{{end}}
{{define "corpo"}}
<header class="topo">
  <h1>O painel nao conseguiu falar com o Docker</h1>
  <p class="host">{{.Host}}</p>
</header>
<p class="nota alerta">{{.Erro}}</p>
<p class="vazio">Enquanto isso nenhuma porta e' liberada pelo proxy, de proposito.
Confira o servico <code>dockerproxy</code> com
<code>docker compose logs dockerproxy</code>.</p>
{{end}}`

// cartaoCtx existe so porque um template nomeado recebe um dado unico, e o
// cartao precisa da entrada e do host junto. A funcao "cartao" o monta.
type cartaoCtx struct {
	E    entrada
	Host string
}

var funcs = template.FuncMap{
	"cartao": func(e entrada, host string) cartaoCtx { return cartaoCtx{e, host} },
}

func novo(nome, corpo string) *template.Template {
	t := template.Must(template.New(nome).Funcs(funcs).Parse(base))
	return template.Must(t.Parse(corpo))
}

var (
	tIndex  = novo("i", tmplIndex)
	tNegado = novo("n", tmplNegado)
	tErro   = novo("e", tmplErro)
	tLogin  = novo("l", tmplLogin)
)

func renderLogin(w io.Writer, host string) {
	if err := tLogin.Execute(w, struct{ Host string }{host}); err != nil {
		log.Printf("render login: %v", err)
	}
}

func renderIndex(w io.Writer, d dadosIndex) {
	if err := tIndex.Execute(w, d); err != nil {
		log.Printf("render index: %v", err)
	}
}

func renderNegado(w io.Writer, d dadosNegado) {
	if err := tNegado.Execute(w, d); err != nil {
		log.Printf("render negado: %v", err)
	}
}

func renderErro(w io.Writer, host string, e error) {
	d := struct {
		Host string
		Erro string
	}{host, e.Error()}
	if err := tErro.Execute(w, d); err != nil {
		log.Printf("render erro: %v", err)
	}
}
