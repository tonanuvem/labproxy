package main

import (
	"os"
	"strconv"
	"strings"
)

// Portas administrativas barradas mesmo quando publicadas por um container.
//
// O CONTEXTO.md (§5) e' explicito: esta lista NAO e' a protecao principal,
// porque o conjunto de portas administrativas que falam HTTP num lab nao e'
// enumeravel de antemao. Ela reduz o raio; quem protege e' o basic_auth.
// O caso que motivou a lista foi a Admin API do Kong na 8001, que o proxy
// entregou inteira durante a validacao do §7.
var denylistPadrao = map[int]string{
	// A 22 passou a importar quando o painel deixou de olhar so para
	// containers: o sshd escuta no host, entao sem esta linha ele entraria
	// na allowlist sozinho.
	22:   "SSH: nao se proxya administracao da VM por HTTP",
	2375: "API do Docker sem TLS: proxya-la e' entregar root na VM",
	2376: "API do Docker com TLS",
	2377: "porta de cluster do Swarm",
	8001: "Admin API do Kong: reconfigura o gateway inteiro",
	8444: "Admin API do Kong em TLS",
	9000: "console de gerenciamento do Keycloak",
	9001: "console de gerenciamento",
}

var denylist = carregarDenylist()

func carregarDenylist() map[int]string {
	d := map[int]string{}
	for p, m := range denylistPadrao {
		d[p] = m
	}
	// PORTAS_BLOQUEADAS acrescenta (nao substitui): "9200,15672"
	for _, s := range strings.Split(os.Getenv("PORTAS_BLOQUEADAS"), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := strconv.Atoi(s); err == nil {
			d[p] = "bloqueada por PORTAS_BLOQUEADAS"
		}
	}
	// PORTAS_LIBERADAS remove da denylist, para o instrutor abrir uma porta
	// administrativa de proposito numa aula: "8001"
	for _, s := range strings.Split(os.Getenv("PORTAS_LIBERADAS"), ",") {
		s = strings.TrimSpace(s)
		if p, err := strconv.Atoi(s); err == nil {
			delete(d, p)
		}
	}
	return d
}

// portaAdministrativa diz se a porta deve ser barrada, e por que.
func portaAdministrativa(p int) (string, bool) {
	// O proxy nao pode proxyar a si mesmo.
	if p == 443 {
		return "e' a porta do proprio proxy", true
	}
	if p == 80 {
		return "e' a porta HTTP do proprio proxy", true
	}
	if m, ok := denylist[p]; ok {
		return m, true
	}
	return "", false
}

// portaValida respeita o mesmo limite do regexp do Caddyfile: 2 a 5 digitos.
func portaValida(s string) (int, bool) {
	if len(s) < 2 || len(s) > 5 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, false
	}
	return p, true
}

// Portas que quase nunca falam HTTP. Nao sao barradas — proxyar um banco por
// HTTP nao leva a nada — mas o painel nao as oferece como link, para o aluno
// nao clicar e levar um 502 sem explicacao.
var naoWebPadrao = map[int]string{
	1433:  "SQL Server: protocolo proprio, nao HTTP",
	1521:  "Oracle: protocolo proprio, nao HTTP",
	2181:  "ZooKeeper: protocolo proprio, nao HTTP",
	3306:  "MySQL/MariaDB: protocolo proprio, nao HTTP",
	5432:  "PostgreSQL: protocolo proprio, nao HTTP",
	5672:  "AMQP (RabbitMQ): protocolo proprio, nao HTTP",
	6379:  "Redis: protocolo proprio, nao HTTP",
	9042:  "Cassandra: protocolo proprio, nao HTTP",
	9092:  "Kafka: protocolo proprio, nao HTTP",
	11211: "Memcached: protocolo proprio, nao HTTP",
	27017: "MongoDB: protocolo proprio, nao HTTP",
}

func portaNaoWeb(p int) (string, bool) {
	m, ok := naoWebPadrao[p]
	return m, ok
}
