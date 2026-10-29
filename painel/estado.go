package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Estado que o aluno controla pela pagina e que precisa sobreviver a um
// restart do container: se a protecao caisse sozinha num 'docker compose
// restart', o lab ficaria aberto sem ninguem perceber.
//
// Um arquivo com "1" ou "0" basta. O diretorio e' criado pelo proxy.sh com
// permissao de escrita, porque o painel roda como nonroot.
type estado struct {
	mu        sync.RWMutex
	caminho   string
	protegido bool
	gravavel  bool
}

func carregarEstado(dir string, padrao bool) *estado {
	e := &estado{caminho: filepath.Join(dir, "protecao"), protegido: padrao}

	if b, err := os.ReadFile(e.caminho); err == nil {
		e.protegido = strings.TrimSpace(string(b)) == "1"
	}

	// Descobre na subida se da' para gravar, para a pagina poder dizer a
	// verdade em vez de oferecer um botao que falha.
	if err := e.gravar(e.protegido); err == nil {
		e.gravavel = true
	}
	return e
}

func (e *estado) gravar(v bool) error {
	b := []byte("0\n")
	if v {
		b = []byte("1\n")
	}
	tmp := e.caminho + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.caminho)
}

func (e *estado) ligado() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.protegido
}

func (e *estado) persistente() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.gravavel
}

func (e *estado) definir(v bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.gravar(v)
	e.protegido = v // vale em memoria mesmo se o disco falhar
	return err
}
