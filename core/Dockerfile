# boxmes-core container image (control plane only; boxd runs on hosts with Incus).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/boxmes-core ./cmd/boxmes-core \
 && for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "-s -w" -o /out/dist/boxd-linux-$a ./cmd/boxd; done

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -h /data boxmes
COPY --from=build /out/boxmes-core /usr/local/bin/boxmes-core
COPY --from=build /out/dist /opt/boxmes/dist
USER boxmes
ENV BOXMES_DATA_DIR=/data BOXMES_ADDR=:8080 BOXMES_DIST_DIR=/opt/boxmes/dist
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/boxmes-core"]
