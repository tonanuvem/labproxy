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
}

type dadosNegado struct {
	Host   string
	Porta  string
	Motivo string
}

func (e entrada) URL(host string) string {
	return "https://" + itoa(e.Porta) + "." + host + "/"
}

// Aberta: o proxy libera a porta. Clicavel: vale oferecer um link.
func (e entrada) Aberta() bool {
	return e.Situacao == acessivel || e.Situacao == noHost || e.Situacao == naoWeb
}
func (e entrada) Clicavel() bool { return e.Situacao == acessivel || e.Situacao == noHost }
func (e entrada) Bloqueada() bool { return e.Situacao == bloqueada }
func (e entrada) Rotulo() string {
	switch e.Situacao {
	case acessivel:
		return "HTTPS"
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
:root{--bg:#fbfbfa;--fg:#1a1a18;--dim:#6b6b66;--linha:#e4e4e0;--card:#fff;--ok:#1b7f4b;--bloq:#a4461f;--acc:#2a5db0}
@media (prefers-color-scheme:dark){:root{--bg:#17181a;--fg:#e8e8e4;--dim:#9a9a94;--linha:#2d2f33;--card:#1f2124;--ok:#4ac17e;--bloq:#e0875c;--acc:#7aa6ef}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.55 ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,sans-serif}
.wrap{max-width:860px;margin:0 auto;padding:40px 16px 64px}
h1{font-size:19px;margin:0 0 4px;font-weight:650;letter-spacing:-.01em}
.host{font:13px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--dim);margin:0 0 28px;word-break:break-all}
h2{font-size:12px;text-transform:uppercase;letter-spacing:.08em;color:var(--dim);font-weight:600;margin:34px 0 10px}
ul{list-style:none;margin:0;padding:0;display:grid;gap:8px}
li{background:var(--card);border:1px solid var(--linha);border-radius:9px;padding:12px 14px}
.linha{display:flex;align-items:baseline;gap:10px;flex-wrap:wrap}
a.porta{font:600 15px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--acc);text-decoration:none}
a.porta:hover{text-decoration:underline}
.porta.off{font:600 15px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--dim)}
.nome{font-weight:550}
.img{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--dim);word-break:break-all}
.tag{font-size:11px;padding:2px 7px;border-radius:20px;border:1px solid var(--linha);color:var(--dim);white-space:nowrap}
.tag.ok{color:var(--ok);border-color:var(--ok)}
.tag.bloq{color:var(--bloq);border-color:var(--bloq)}
.motivo{font-size:13px;color:var(--dim);margin-top:5px}
.nota{border-left:2px solid var(--linha);padding:2px 0 2px 13px;color:var(--dim);font-size:13.5px;margin:12px 0}
.vazio{color:var(--dim);font-size:14px}
code{font:12.5px ui-monospace,SFMono-Regular,Menlo,monospace;background:var(--card);border:1px solid var(--linha);border-radius:4px;padding:1px 5px}
footer{margin-top:44px;padding-top:16px;border-top:1px solid var(--linha);color:var(--dim);font-size:12.5px}
.prot{margin:0 0 26px;padding:14px 16px;border-radius:9px;border:1px solid var(--linha);background:var(--card)}
.prot.aberto{border-color:var(--bloq)}
.prot h3{margin:0 0 5px;font-size:14px;font-weight:650}
.prot.aberto h3{color:var(--bloq)}
.prot p{margin:0 0 11px;font-size:13.5px;color:var(--dim)}
.prot form{margin:0}
button{font:inherit;font-size:13px;font-weight:550;padding:7px 14px;border-radius:7px;
  border:1px solid var(--linha);background:var(--bg);color:var(--fg);cursor:pointer}
button:hover{border-color:var(--acc);color:var(--acc)}
button.forte{background:var(--bloq);border-color:var(--bloq);color:#fff}
button.forte:hover{opacity:.88;color:#fff}
</style></head><body><div class="wrap">{{template "corpo" .}}</div></body></html>`

const tmplIndex = `
{{define "titulo"}}Lab de {{.Host}}{{end}}
{{define "corpo"}}
<h1>Aplicacoes do seu lab</h1>
<p class="host">{{.Host}}</p>

{{if .Protegido}}
<div class="prot">
  <h3>Lab protegido por senha</h3>
  <p>Quem abrir qualquer endereco deste lab precisa entrar com
  <code>{{.Usuario}}</code> e a senha da turma.</p>
  <form method="post" action="/_protecao">
    <input type="hidden" name="acao" value="desativar">
    <button type="submit">Desligar a protecao</button>
  </form>
</div>
{{else}}
<div class="prot aberto">
  <h3>Este lab esta aberto na internet</h3>
  <p>Qualquer pessoa que descubra este endereco ve esta pagina e alcanca as
  aplicacoes abaixo. O nome aparece nos registros publicos de certificado,
  entao achar nao e' dificil. Se algo aqui aceita comandos — um
  <code>code-server</code> sem senha, por exemplo — quem chegar executa
  comandos nesta VM.</p>
  {{if .TemSenha}}
  <form method="post" action="/_protecao">
    <input type="hidden" name="acao" value="ativar">
    <button class="forte" type="submit">Proteger com a senha da turma</button>
  </form>
  {{else}}
  <p>Nao ha senha configurada nesta VM. Suba o proxy de novo com
  <code>LABPROXY_SENHA=...</code> para poder proteger.</p>
  {{end}}
</div>
{{end}}
{{if not .Persistente}}
<p class="nota">O painel nao consegue gravar o estado da protecao em disco:
se o container reiniciar, ela volta ao padrao.</p>
{{end}}

{{$host := .Host}}
{{$ac := false}}{{range .Inv.Entradas}}{{if .Aberta}}{{$ac = true}}{{end}}{{end}}

<h2>Abertas pelo proxy</h2>
{{if $ac}}
<ul>{{range .Inv.Entradas}}{{if .Aberta}}
  <li><div class="linha">
    {{if .Clicavel}}<a class="porta" href="{{.URL $host}}" target="_blank" rel="noopener noreferrer">{{.Porta}}</a>
    {{else}}<span class="porta off">{{.Porta}}</span>{{end}}
    <span class="nome">{{.Container}}</span>
    <span class="tag{{if .Clicavel}} ok{{end}}">{{.Rotulo}}</span>
  </div>
  <div class="img">{{.Imagem}}{{if .PortaInt}} &middot; container :{{.PortaInt}}{{end}}</div>
  {{if .Motivo}}<div class="motivo">{{.Motivo}}</div>{{end}}</li>
{{end}}{{end}}</ul>
{{else}}
<p class="vazio">Nada escutando ainda. Suba um container com
<code>docker compose up -d</code>, ou um processo direto na VM, e recarregue.</p>
{{end}}

{{$out := false}}{{range .Inv.Entradas}}{{if not .Aberta}}{{$out = true}}{{end}}{{end}}
{{if $out}}
<h2>Nao expostas</h2>
<ul>{{range .Inv.Entradas}}{{if not .Aberta}}
  <li><div class="linha">
    <span class="porta off">{{if .Porta}}{{.Porta}}{{else}}&mdash;{{end}}</span>
    <span class="nome">{{.Container}}</span>
    <span class="tag{{if .Bloqueada}} bloq{{end}}">{{.Rotulo}}</span>
  </div>
  <div class="img">{{.Imagem}} &middot; container :{{.PortaInt}}</div>
  {{if .Motivo}}<div class="motivo">{{.Motivo}}</div>{{end}}</li>
{{end}}{{end}}</ul>
{{end}}

<p class="nota">Container so aparece aqui com porta <strong>publicada</strong>
(<code>-p</code>). <code>EXPOSE</code> sozinho nao basta — e' a confusao mais
comum. Processo rodando direto na VM, fora do Docker, aparece como
<em>no host</em>. Uma porta presa ao <code>127.0.0.1</code> fica inalcancavel
de fora de proposito, e por isso nao aparece como aberta.</p>

<footer>labproxy &middot; o endereco de cada app e'
<code>https://&lt;porta&gt;.{{.Host}}</code></footer>
{{end}}`

const tmplNegado = `
{{define "titulo"}}Porta {{.Porta}} nao liberada{{end}}
{{define "corpo"}}
<h1>Porta {{.Porta}} nao esta liberada</h1>
<p class="host">{{.Porta}}.{{.Host}}</p>
<p class="nota">{{.Motivo}}</p>
<p class="vazio">Veja o que esta no ar em <a href="https://{{.Host}}/">{{.Host}}</a>.</p>
{{end}}`

const tmplLogin = `
{{define "titulo"}}Lab protegido{{end}}
{{define "corpo"}}
<h1>Este lab esta protegido</h1>
<p class="host">{{.Host}}</p>
<p class="nota">Entre com o usuario e a senha da turma. Se voce e' o dono desta
VM e nao lembra a senha, ela e' a mesma que o <code>dns.sh</code> usa.</p>
{{end}}`

const tmplErro = `
{{define "titulo"}}Painel sem Docker{{end}}
{{define "corpo"}}
<h1>O painel nao conseguiu falar com o Docker</h1>
<p class="host">{{.Host}}</p>
<p class="nota">{{.Erro}}</p>
<p class="vazio">Enquanto isso nenhuma porta e' liberada pelo proxy, de proposito.
Confira o servico <code>dockerproxy</code> com
<code>docker compose logs dockerproxy</code>.</p>
{{end}}`

var (
	tIndex  = template.Must(template.Must(template.New("i").Parse(base)).Parse(tmplIndex))
	tNegado = template.Must(template.Must(template.New("n").Parse(base)).Parse(tmplNegado))
	tErro   = template.Must(template.Must(template.New("e").Parse(base)).Parse(tmplErro))
	tLogin  = template.Must(template.Must(template.New("l").Parse(base)).Parse(tmplLogin))
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
