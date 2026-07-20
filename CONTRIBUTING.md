# Contribuir a Atlas

¡Gracias por tu interés! Atlas es open source (Apache-2.0) y crece con la
comunidad. Esta guía es corta a propósito.

## Antes de escribir código

- Para algo pequeño (bug, doc, refactor menor): abre un PR directo.
- Para una feature: **abre un issue primero** con la propuesta, para no duplicar
  esfuerzo ni chocar con el roadmap (ver `docs/ARCHITECTURE.md`).

## Entorno

Requisitos: **Go 1.22+** y **Node 20+**.

```bash
git clone <tu-fork>
cd atlas
./scripts/dev.sh        # arranca todo el stack en local
```

## Antes de cada PR

```bash
make fmt      # gofmt
make vet      # go vet
make test     # go test -race
cd web && npm run build   # typecheck + build de la GUI
```

El CI corre exactamente esto (más `govulncheck`), así que si pasa en local,
pasa en el PR.

## Estilo

- **Go**: sigue `gofmt` y `go vet`. Nombres cortos, errores envueltos con
  contexto, sin dependencias externas salvo necesidad justificada.
- **TypeScript**: modo estricto (ya activo). Tipos que reflejen `pkg/api`.
- **Commits**: mensaje imperativo y claro (`feat:`, `fix:`, `docs:`, `chore:`).

## Contrato de la API

`pkg/api/types.go` es la fuente de verdad. Si lo cambias, actualiza también
`web/src/api.ts` en el mismo PR.

## Licencia

Al contribuir aceptas que tu aporte se publique bajo Apache-2.0.
