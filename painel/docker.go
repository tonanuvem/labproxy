package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Porta publicada, como o Docker reporta em /containers/json.
type portaDocker struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type containerDocker struct {
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Ports  []portaDocker     `json:"Ports"`
	Labels map[string]string `json:"Labels"`
}

// Como uma porta se apresenta ao aluno.
type situacao int

const (
	acessivel   situacao = iota // publicada por container: o proxy alcanca, e e' web
	noHost                      // processo do host, fora do Docker (code-server, npm)
	naoWeb                      // alcancavel, mas a porta nao fala HTTP (banco, fila)
	bloqueada                   // publicada, mas na denylist administrativa
	soLoopback                  // -p 127.0.0.1:X:Y  -> inalcancavel (intencional, §5)
	soExpose                    // EXPOSE sem -p     -> inalcancavel (confusao comum, §8)
)

type entrada struct {
	Container string
	Imagem    string
	Status    string
	Porta     int // porta no host (0 quando soExpose)
	PortaInt  int // porta dentro do container
	Situacao  situacao
	Motivo    string
}

type inventario struct {
	Entradas  []entrada
	Permitido map[int]bool
	Em        time.Time

	// portas que algum container publicou, para nao duplicar na varredura
	vistaEmContainer map[int]bool
}

type docker struct {
	host string
	cli  *http.Client

	mu     sync.Mutex
	cache  *inventario
	valido time.Duration
}

func novoDocker(host string, ttl time.Duration) *docker {
	return &docker{
		host:   host,
		cli:    &http.Client{Timeout: 5 * time.Second},
		valido: ttl,
	}
}

// inventario devolve o estado dos containers, com cache curto para nao
// martelar o socket a cada requisicao do forward_auth.
func (d *docker) inventario() (*inventario, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cache != nil && time.Since(d.cache.Em) < d.valido {
		return d.cache, nil
	}

	req, err := http.NewRequest("GET", d.host+"/containers/json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker respondeu %d", resp.StatusCode)
	}

	var cs []containerDocker
	if err := json.NewDecoder(resp.Body).Decode(&cs); err != nil {
		return nil, err
	}

	inv := &inventario{
		Permitido:        map[int]bool{},
		vistaEmContainer: map[int]bool{},
		Em:               time.Now(),
	}
	visto := map[string]bool{} // dedupe: Docker lista IPv4 e IPv6 da mesma porta

	for _, c := range cs {
		// O aluno nao quer ver a infraestrutura do proxy no inventario dele.
		if c.Labels["com.docker.compose.project"] == projetoProprio {
			continue
		}
		nome := "?"
		if len(c.Names) > 0 {
			nome = trimBarra(c.Names[0])
		}
		if len(c.Ports) == 0 {
			continue
		}
		for _, p := range c.Ports {
			if p.Type != "tcp" {
				continue
			}
			e := entrada{Container: nome, Imagem: c.Image, Status: c.Status, PortaInt: p.PrivatePort}

			switch {
			case p.PublicPort == 0:
				e.Situacao = soExpose
				e.Motivo = "sem -p: o container expoe a porta, mas ela nao foi publicada no host"
			case ehLoopback(p.IP):
				e.Porta = p.PublicPort
				e.Situacao = soLoopback
				e.Motivo = "publicada so em " + p.IP + ": inalcancavel pelo proxy, por desenho"
			default:
				e.Porta = p.PublicPort
				e.Situacao = acessivel
			}

			chave := fmt.Sprintf("%s/%d/%d/%d", nome, e.Porta, e.PortaInt, e.Situacao)
			if visto[chave] {
				continue
			}
			visto[chave] = true
			if e.Porta != 0 {
				inv.vistaEmContainer[e.Porta] = true
			}
			inv.Entradas = append(inv.Entradas, e)
		}
	}

	// A allowlist e' o conjunto de portas realmente publicadas na interface
	// do host, menos as administrativas. Ver §5 do CONTEXTO.md.
	for i := range inv.Entradas {
		e := &inv.Entradas[i]
		if e.Situacao != acessivel {
			continue
		}
		if motivo, barrada := portaAdministrativa(e.Porta); barrada {
			e.Situacao = bloqueada
			e.Motivo = motivo
			continue
		}
		// Proxyar um banco por HTTP e' inofensivo e inutil: liberamos, mas nao
		// oferecemos link, senao o aluno clica e recebe um 502 sem explicacao.
		if m, naoFala := portaNaoWeb(e.Porta); naoFala {
			e.Situacao = naoWeb
			e.Motivo = m
		}
		inv.Permitido[e.Porta] = true
	}

	// Agora as portas que nao sao de container nenhum. O aluno roda o
	// code-server e um "npm run dev" direto na VM, e isso tambem e' o lab dele.
	for porta := range varrerHost(portasVarredura()) {
		if inv.vistaEmContainer[porta] {
			continue // o docker-proxy da porta publicada ja apareceu acima
		}
		e := entrada{
			Container: "processo do host",
			Imagem:    "fora do Docker",
			Porta:     porta,
			Situacao:  noHost,
		}
		if motivo, barrada := portaAdministrativa(porta); barrada {
			e.Situacao = bloqueada
			e.Motivo = motivo
		} else if m, naoFala := portaNaoWeb(porta); naoFala {
			e.Situacao = naoWeb
			e.Motivo = m
			inv.Permitido[porta] = true
		} else {
			inv.Permitido[porta] = true
		}
		inv.Entradas = append(inv.Entradas, e)
	}

	sort.Slice(inv.Entradas, func(i, j int) bool {
		a, b := inv.Entradas[i], inv.Entradas[j]
		if a.Situacao != b.Situacao {
			return a.Situacao < b.Situacao
		}
		if a.Porta != b.Porta {
			return a.Porta < b.Porta
		}
		return a.Container < b.Container
	})

	d.cache = inv
	return inv, nil
}

func trimBarra(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}

var projetoProprio = env("LABPROXY_PROJETO", "labproxy")

func ehLoopback(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1" || ip == "localhost"
}
