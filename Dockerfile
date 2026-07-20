# Dockerfile multi-etapa: compila ambos binarios Go y produce dos imágenes
# mínimas (distroless, sin shell, usuario no-root) — una por target.
#
#   docker build --target controlplane -t atlas-controlplane .
#   docker build --target agent        -t atlas-agent .

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
# Sin dependencias externas: build reproducible y offline.
RUN CGO_ENABLED=0 go build -trimpath -o /out/controlplane ./cmd/controlplane \
 && CGO_ENABLED=0 go build -trimpath -o /out/agent        ./cmd/agent

# --- control plane ---
FROM gcr.io/distroless/static-debian12:nonroot AS controlplane
COPY --from=build /out/controlplane /controlplane
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/controlplane"]

# --- agente ---
FROM gcr.io/distroless/static-debian12:nonroot AS agent
COPY --from=build /out/agent /agent
USER nonroot:nonroot
ENTRYPOINT ["/agent"]
