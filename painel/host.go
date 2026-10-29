package main

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Descoberta de portas que NAO sao de container.
//
// O painel lia so o Docker, e isso deixava de fora tudo que o aluno roda
// direto na VM — o code-server do lab, um "npm run dev", um Flask. Eram 403.
//
// A pergunta certa nao e' "o Docker conhece esta porta?", e sim "ha algo
// escutando onde o Caddy vai bater?". Entao perguntamos do mesmo jeito que o
// Caddy pergunta: abrindo uma conexao TCP para host.docker.internal:<porta>.
//
// Isso preserva de graca a regra do §5: uma porta publicada so em 127.0.0.1
// nao atende por host.docker.internal, o discador falha, e ela continua
// inalcancavel — sem precisar ler /proc nem dar mais privilegio ao painel.

const alvoHost = "host.docker.internal"

// Portas varridas para montar a lista do painel. A liberacao em si nao depende
// desta lista: o /_authz disca sob demanda, entao uma porta fora dela funciona
// do mesmo jeito, so nao aparece com link na pagina.
var portasVarreduraPadrao = []int{
	80, 443, 3000, 3001, 4000, 4200, 5000, 5001, 5173, 5174, 7860,
	8000, 8001, 8080, 8081, 8082, 8088, 8099, 8100, 8181, 8500, 8501,
	8888, 9000, 9090, 9091, 9200, 9999, 10000,
}

func portasVarredura() []int {
	extra := strings.TrimSpace(env("PORTAS_HOST", ""))
	if extra == "" {
		return portasVarreduraPadrao
	}
	vistas := map[int]bool{}
	var out []int
	add := func(p int) {
		if p > 0 && p < 65536 && !vistas[p] {
			vistas[p] = true
			out = append(out, p)
		}
	}
	for _, p := range portasVarreduraPadrao {
		add(p)
	}
	for _, s := range strings.Split(extra, ",") {
		if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			add(p)
		}
	}
	sort.Ints(out)
	return out
}

var tempoDiscagem = func() time.Duration {
	if ms, err := strconv.Atoi(env("TIMEOUT_DISCAGEM_MS", "400")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return 400 * time.Millisecond
}()

// escutando diz se ha algo aceitando conexao naquela porta do host.
func escutando(porta int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(alvoHost, strconv.Itoa(porta)), tempoDiscagem)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// varrerHost disca em paralelo as portas da lista e devolve as que atendem.
func varrerHost(portas []int) map[int]bool {
	const paralelas = 32
	sem := make(chan struct{}, paralelas)
	var mu sync.Mutex
	var wg sync.WaitGroup
	vivas := map[int]bool{}

	for _, p := range portas {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if escutando(p) {
				mu.Lock()
				vivas[p] = true
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return vivas
}
