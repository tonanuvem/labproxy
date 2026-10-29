package main

import (
	"crypto/subtle"
	"net/http"
)

// A autenticacao saiu do Caddyfile e veio para ca.
//
// O motivo: o aluno liga e desliga a protecao pela pagina, e mexer no
// Caddyfile em tempo de execucao exigiria o painel escrevendo a config do
// Caddy e chamando a admin API para recarregar. Como o Caddy ja consulta o
// painel por forward_auth a cada requisicao, e' mais simples o painel ser a
// autoridade unica — de autenticacao e de allowlist.
//
// Num 401, o Caddy devolve ao cliente a resposta do forward_auth como ela e',
// cabecalhos inclusive, entao o WWW-Authenticate chega ao navegador e o
// prompt aparece normalmente.

func (a *app) credencialOK(r *http.Request) bool {
	if a.senha == "" {
		return false // sem senha configurada nao ha como autenticar ninguem
	}
	u, p, ok := r.BasicAuth()
	if !ok {
		return false
	}
	iguaisU := subtle.ConstantTimeCompare([]byte(u), []byte(a.usuario))
	iguaisP := subtle.ConstantTimeCompare([]byte(p), []byte(a.senha))
	return iguaisU == 1 && iguaisP == 1
}

// liberado diz se a requisicao pode seguir: ou a protecao esta desligada, ou
// a credencial confere.
func (a *app) liberado(r *http.Request) bool {
	return !a.estado.ligado() || a.credencialOK(r)
}

func pedirCredencial(w http.ResponseWriter, host string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="lab de `+host+`", charset="UTF-8"`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	renderLogin(w, host)
}
