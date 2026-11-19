# Core image for Render (web service, Docker runtime, build context = core/).
# Same binaries as core/Dockerfile (core + boxd linux amd64/arm64 served at /dl/)
# plus cloudflared, so the named tunnel can carry gw.<domain> and *.<domain>
# (wildcard TLS stays on Cloudflare). start.sh runs cloudflared only when
# TUNNEL_TOKEN is set; without it the service answers on its onrender.com URL.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/boxaz-core ./cmd/boxaz-core \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/box ./cmd/box \
 && for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" -o /out/dist/boxd-linux-$a ./cmd/boxd; done

FROM alpine:3.20
ARG TARGETARCH=amd64
ARG CLOUDFLARED_VERSION=latest
RUN apk add --no-cache ca-certificates curl su-exec tini \
 && adduser -D -h /data app \
 && if [ "$CLOUDFLARED_VERSION" = latest ]; then u=https://github.com/cloudflare/cloudflared/releases/latest/download; else u=https://github.com/cloudflare/cloudflared/releases/download/$CLOUDFLARED_VERSION; fi \
 && curl -fsSL -o /usr/local/bin/cloudflared "$u/cloudflared-linux-${TARGETARCH:-amd64}" \
 && chmod +x /usr/local/bin/cloudflared && /usr/local/bin/cloudflared --version
COPY --from=build /out/boxaz-core /out/box /usr/local/bin/
COPY --from=build /out/dist /opt/app/dist
COPY deploy/render/start.sh /usr/local/bin/start.sh
ENV BOXAZ_DATA_DIR=/data BOXAZ_ADDR=:8091 PORT=8091 BOXAZ_DIST_DIR=/opt/app/dist
EXPOSE 8091
ENTRYPOINT ["/sbin/tini", "--", "/bin/sh", "/usr/local/bin/start.sh"]
