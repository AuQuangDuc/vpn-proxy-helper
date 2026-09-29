FROM golang:1.23-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/vpn-proxy-helper \
    .

FROM scratch

COPY --from=build /out/vpn-proxy-helper /vpn-proxy-helper

USER 65532:65532

ENTRYPOINT ["/vpn-proxy-helper"]
