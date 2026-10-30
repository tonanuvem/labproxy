// Painel do labproxy: lista os containers e processos do aluno, decide quais
// portas o Caddy pode proxyar, e guarda se o lab esta protegido por senha.
// Nao e' um proxy — o roteamento por porta e' do Caddy (ver §3.1 e §4 do
// CONTEXTO.md).
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
	host    string // nome base do aluno: joao.aluno.lab.tonanuvem.com
	usuario string
	senha   string
	tunel   string // caminho do tunel SSH; vazio quando desligado
	certAmb string // "producao", "staging", "autoassinado" ou desconhecido
	certExp string
	docker  *docker
	estado  *estado
}

func env(k, padrao string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return padrao
}

func main() {
	a := &app{
		host:    env("LABPROXY_HOST", "localhost"),
		usuario: env("LABPROXY_USUARIO", "aluno"),
		senha:   os.Getenv("LABPROXY_SENHA"),
		tunel:   os.Getenv("LABPROXY_TUNEL_CAMINHO"),
		certAmb: os.Getenv("LABPROXY_CERT_AMBIENTE"),
		certExp: os.Getenv("LABPROXY_CERT_EXPIRA"),
		docker:  novoDocker(env("DOCKER_API", "http://dockerproxy:2375"), 3*time.Second),
		estado:  carregarEstado(env("DIR_ESTADO", "/estado"), env("PROTECAO_INICIAL", "0") == "1"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_authz", a.authz)
	mux.HandleFunc("/_protecao", a.protecao)
	mux.HandleFunc("/healthz", a.healthz)
	mux.HandleFunc("/", a.index)

	addr := ":" + env("PORTA", "8000")
	log.Printf("painel de %s escutando em %s (protecao=%v, persistente=%v)",
		a.host, addr, a.estado.ligado(), a.estado.persistente())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// healthz nao exige credencial: e' o compose que pergunta, nao a internet.
func (a *app) healthz(w http.ResponseWriter, r *http.Request) {
	if _, err := a.docker.inventario(); err != nil {
		http.Error(w, "docker inacessivel: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authz responde ao forward_auth do Caddy: 204 libera, 401 pede senha,
// 403 barra a porta.
//
// Fail-closed de proposito. Se o Docker estiver inacessivel o painel nao sabe
// quais portas sao legitimas, e liberar nessa situacao seria abrir qualquer
// porta do host para a internet.
func (a *app) authz(w http.ResponseWriter, r *http.Request) {
	if !a.liberado(r) {
		pedirCredencial(w, a.host)
		return
	}

	bruta := r.URL.Query().Get("porta")
	p, ok := portaValida(bruta)
	if !ok {
		http.Error(w, "porta invalida", http.StatusForbidden)
		return
	}

	inv, err := a.docker.inventario()
	if err != nil {
		log.Printf("authz porta=%d: docker inacessivel: %v", p, err)
		http.Error(w, "painel nao conseguiu falar com o Docker; nenhuma porta e' liberada",
			http.StatusServiceUnavailable)
		return
	}

	if inv.Permitido[p] {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// A varredura que monta a pagina cobre uma lista de portas comuns. Uma
	// porta fora dela continua legitima se houver algo escutando, entao
	// perguntamos na hora — do mesmo jeito que o Caddy vai perguntar logo em
	// seguida, abrindo uma conexao para host.docker.internal.
	motivoAdmin, barrada := portaAdministrativa(p)
	if !barrada && escutando(p) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	motivo := "nada escutando na porta " + bruta + " na interface do host"
	if barrada {
		motivo = motivoAdmin
	} else {
		for _, e := range inv.Entradas {
			if e.Porta == p && e.Motivo != "" {
				motivo = e.Motivo
				break
			}
		}
	}
	log.Printf("authz porta=%d negada: %s", p, motivo)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	renderNegado(w, dadosNegado{Host: a.host, Porta: bruta, Motivo: motivo})
}

// protecao liga ou desliga a senha do lab.
//
// Ligar nao exige credencial, e isso e' de proposito: com o lab aberto,
// qualquer um que caia na pagina pode fecha-la, e o pior que faz e' deixar o
// lab mais seguro. Nao da' para trancar o aluno para fora, porque a senha e'
// sempre a da turma, que ele conhece.
//
// Desligar exige credencial, senao a protecao nao protegeria nada.
func (a *app) protecao(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	acao := r.FormValue("acao")

	switch acao {
	case "ativar":
		if a.senha == "" {
			http.Error(w, "nao ha senha configurada nesta VM; suba o proxy com LABPROXY_SENHA",
				http.StatusConflict)
			return
		}
		if err := a.estado.definir(true); err != nil {
			log.Printf("protecao: ativei em memoria mas nao gravei: %v", err)
		}
	case "desativar":
		if !a.credencialOK(r) {
			pedirCredencial(w, a.host)
			return
		}
		if err := a.estado.definir(false); err != nil {
			log.Printf("protecao: desativei em memoria mas nao gravei: %v", err)
		}
	default:
		http.Error(w, "acao invalida", http.StatusBadRequest)
		return
	}

	log.Printf("protecao agora: %v", a.estado.ligado())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !a.liberado(r) {
		pedirCredencial(w, a.host)
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
	renderIndex(w, dadosIndex{
		Host:        a.host,
		Inv:         inv,
		Protegido:   a.estado.ligado(),
		Persistente: a.estado.persistente(),
		TemSenha:    a.senha != "",
		Usuario:     a.usuario,
		Tunel:       a.tunel,
		UsuarioSSH:  env("LABPROXY_USUARIO_SSH", "ubuntu"),
		CertAmb:     a.certAmb,
		CertExp:     a.certExp,
	})
}
