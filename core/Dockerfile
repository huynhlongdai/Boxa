# boxa-core container image (control plane only; boxd runs on hosts with Incus).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/boxa-core ./cmd/boxa-core \
 && for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" -o /out/dist/boxd-linux-$a ./cmd/boxd; done

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -h /data boxa
COPY --from=build /out/boxa-core /usr/local/bin/boxa-core
COPY --from=build /out/dist /opt/boxa/dist
USER boxa
ENV BOXA_DATA_DIR=/data BOXA_ADDR=:8080 BOXA_DIST_DIR=/opt/boxa/dist
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/boxa-core"]
