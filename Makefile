# Atlas — atajos de desarrollo.
# Requisitos: Go 1.22+ y Node 20+.

.PHONY: help build controlplane agent run-controlplane run-agent \
        web-install web-dev tidy vet fmt clean

help: ## Muestra esta ayuda
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	 awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: controlplane agent ## Compila ambos binarios en ./bin

controlplane: ## Compila el control plane
	go build -o bin/controlplane ./cmd/controlplane

agent: ## Compila el agente
	go build -o bin/agent ./cmd/agent

run-controlplane: ## Arranca el control plane (:8080)
	go run ./cmd/controlplane

run-agent: ## Arranca un agente de ejemplo (on-prem)
	go run ./cmd/agent --name "on-prem lab" --provider onprem --control-plane http://localhost:8080

web-install: ## Instala dependencias de la GUI
	cd web && npm install

web-dev: ## Arranca la GUI en modo desarrollo (:5173)
	cd web && npm run dev

tidy: ## go mod tidy
	go mod tidy

vet: ## go vet
	go vet ./...

fmt: ## gofmt
	gofmt -w cmd internal pkg

clean: ## Limpia binarios
	rm -rf bin
