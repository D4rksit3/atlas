# Atlas — atajos de desarrollo.
# Requisitos: Go 1.22+ y Node 20+.

.PHONY: help up install build controlplane agent run-controlplane run-agent \
        web-install web-dev test test-kube test-hubble test-deploy test-mtls test-rotation test-revocation test-netpol test-ingress test-postgres test-actions test-oidc test-audit test-annotations test-argocd test-gitops test-sync test-addons test-helm test-values test-upgrade test-install vet fmt lint tidy docker-up docker-down clean

help: ## Muestra esta ayuda
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	 awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

up: ## Arranca TODO el stack en local (control plane + agentes + GUI)
	./scripts/dev.sh

install: ## Instala Atlas en un clúster (pregunta dominio + local/público)
	./scripts/install.sh

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

test-hubble: ## E2E: kind+Cilium+Hubble y verifica los enlaces reales del mapa (necesita docker+kind+kubectl+cilium)
	./scripts/test-hubble.sh

test-deploy: ## E2E: despliega Atlas DENTRO de kind (control plane+GUI+agente) y verifica el circuito
	./scripts/test-deploy.sh

test-mtls: ## E2E: verifica el mTLS agente↔control plane (no necesita clúster)
	./scripts/test-mtls.sh

test-rotation: ## E2E: rotación de certs (hojas cortas por CLI + hot-reload sin reinicio)
	./scripts/test-rotation.sh

test-revocation: ## E2E: revocación inmediata de certs (CRL firmada por la CA + corte en caliente)
	./scripts/test-revocation.sh

test-netpol: ## E2E: acotado de egress por componente (NetworkPolicy default-deny, k3d)
	./scripts/test-netpol.sh

test-ingress: ## E2E: publicar servicios — ingress-nginx + cert-manager + ClusterIssuer desde la GUI (k3d)
	./scripts/test-ingress.sh

test-postgres: ## E2E: store Postgres — multi-réplica + persistencia (levanta su propio Postgres)
	./scripts/test-postgres.sh

test-actions: ## E2E: operar cargas desde la GUI (escalar/reiniciar) contra kind
	./scripts/test-actions.sh

test-oidc: ## E2E: auth de la GUI (OIDC+RBAC) con un IdP de prueba (no necesita clúster)
	./scripts/test-oidc.sh

test-audit: ## E2E: registro de auditoría con atribución de usuario (OIDC)
	./scripts/test-audit.sh

test-annotations: ## E2E: editar el mapa (anotaciones) con RBAC + auditoría
	./scripts/test-annotations.sh

test-argocd: ## E2E: instalar ArgoCD desde la GUI (acción install) contra k3d
	./scripts/test-argocd.sh

test-gitops: ## E2E: registrar un proyecto GitOps desde la GUI y verlo sincronizar
	./scripts/test-gitops.sh

test-sync: ## E2E: sincronizar / revertir un proyecto GitOps desde la GUI
	./scripts/test-sync.sh

test-addons: ## E2E: catálogo de complementos (instalar Kyverno) desde la GUI
	./scripts/test-addons.sh

test-helm: ## E2E: instalar un complemento Helm (Falco) desde la GUI
	./scripts/test-helm.sh

test-values: ## E2E: valores editables al instalar (contraseña Grafana, retención)
	./scripts/test-values.sh

test-upgrade: ## E2E: editar valores de un complemento instalado (helm upgrade)
	./scripts/test-upgrade.sh

test-install: ## E2E: instalador (dominio + local/público) y enrutado por Ingress
	./scripts/test-install.sh

certs: ## Genera una PKI de desarrollo en ./certs (CA + servidor + un agente)
	go run ./cmd/atlas-certs bundle --out certs --hosts localhost,127.0.0.1

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
