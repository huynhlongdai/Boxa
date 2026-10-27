# boxaz-core container image (control plane only; boxd runs on hosts with Incus).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/boxaz-core ./cmd/boxaz-core \
 && for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" -o /out/dist/boxd-linux-$a ./cmd/boxd; done

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -h /data boxaz
COPY --from=build /out/boxaz-core /usr/local/bin/boxaz-core
COPY --from=build /out/dist /opt/boxaz/dist
USER boxaz
ENV BOXAZ_DATA_DIR=/data BOXAZ_ADDR=:8080 BOXAZ_DIST_DIR=/opt/boxaz/dist
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/boxaz-core"]
