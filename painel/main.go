// Painel do labproxy: lista os containers do aluno e decide quais portas o
// Caddy pode proxyar. Nao e' um proxy — o roteamento por porta e' do Caddy
// (ver §3.1 e §4 do CONTEXTO.md). Este servico so desenha a pagina e responde
// ao forward_auth.
//
// Sem dependencias fora da stdlib: a VM do lab nem sempre tem rede boa para
// baixar modulos, e menos codigo de terceiros num servico exposto e' melhor.
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

type app struct {
	host   string // nome base do aluno: joao.aluno.lab.tonanuvem.com
	docker *docker
}

func env(k, padrao string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return padrao
}

func main() {
	a := &app{
		host:   env("LABPROXY_HOST", "localhost"),
		docker: novoDocker(env("DOCKER_API", "http://dockerproxy:2375"), 3*time.Second),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_authz", a.authz)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.docker.inventario(); err != nil {
			http.Error(w, "docker inacessivel: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", a.index)

	addr := ":" + env("PORTA", "8000")
	log.Printf("painel de %s escutando em %s", a.host, addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// authz responde ao forward_auth do Caddy: 204 libera, 403 barra.
//
// Fail-closed de proposito. Se o Docker estiver inacessivel o painel nao sabe
// quais portas sao legitimas, e liberar nessa situacao seria abrir qualquer
// porta do host para a internet.
func (a *app) authz(w http.ResponseWriter, r *http.Request) {
	bruta := r.URL.Query().Get("porta")
	p, ok := portaValida(bruta)
	if !ok {
		http.Error(w, "porta invalida", http.StatusForbidden)
		return
	}

	inv, err := a.docker.inventario()
	if err != nil {
		log.Printf("authz porta=%d: docker inacessivel: %v", p, err)
		http.Error(w, "painel nao conseguiu falar com o Docker; nenhuma porta e' liberada", http.StatusServiceUnavailable)
		return
	}

	if inv.Permitido[p] {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	motivo := "nenhum container publicou a porta " + bruta + " na interface do host"
	for _, e := range inv.Entradas {
		if e.Porta == p && e.Motivo != "" {
			motivo = e.Motivo
			break
		}
	}
	log.Printf("authz porta=%d negada: %s", p, motivo)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	renderNegado(w, dadosNegado{Host: a.host, Porta: bruta, Motivo: motivo})
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	inv, err := a.docker.inventario()
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		renderErro(w, a.host, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	renderIndex(w, dadosIndex{Host: a.host, Inv: inv})
}
