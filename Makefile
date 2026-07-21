# Atlas — atajos de desarrollo.
# Requisitos: Go 1.22+ y Node 20+.

.PHONY: help up build controlplane agent run-controlplane run-agent \
        web-install web-dev test test-kube vet fmt lint tidy docker-up docker-down clean

help: ## Muestra esta ayuda
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	 awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

up: ## Arranca TODO el stack en local (control plane + agentes + GUI)
	./scripts/dev.sh

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

test: ## Corre los tests de Go (con -race)
	go test -race -cover ./...

test-kube: ## E2E: levanta kind, corre el colector kube y verifica el mapa (necesita docker+kind+kubectl)
	./scripts/test-kube.sh

vet: ## go vet
	go vet ./...

fmt: ## gofmt
	gofmt -w cmd internal pkg

lint: ## golangci-lint (si está instalado)
	golangci-lint run

tidy: ## go mod tidy
	go mod tidy

docker-up: ## Levanta todo con Docker (sin instalar Go/Node)
	docker compose up --build

docker-down: ## Baja el stack de Docker
	docker compose down

clean: ## Limpia binarios
	rm -rf bin
