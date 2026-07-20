# GUI: compila con Node y sirve el bundle estático con nginx, que además hace de
# proxy hacia el control plane. Contexto de build: la raíz del repo.
#
#   docker build -f web/Dockerfile -t atlas-web .

FROM node:20-alpine AS build
WORKDIR /app
COPY web/package.json ./
RUN npm install --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM nginx:1.27-alpine
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
