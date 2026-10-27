# One image per command: docker build --build-arg CMD=probe .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG CMD=server
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
# Go 1.26+ adds the P-curve ML-KEM key exchanges (SecP256r1MLKEM768,
# SecP384r1MLKEM1024) to a default ClientHello. One book's edge answers that
# ClientHello with its branded HTML block page and HTTP 403 on the REST API,
# while accepting the WebSocket upgrade from the same process, so the snapshot
# never seeds and the book looks "down". tlssecpmlkem=0 removes exactly those
# two groups and keeps the X25519/ML-KEM hybrid; certificate verification and
# TLS 1.3 are untouched. It must be set before the process starts: GODEBUG is
# read at init, so this cannot be done from inside main.
# Evidence and the caveat: README §13, research/BETMGM_DOCKER_DIAGNOSIS.md.
ENV GODEBUG=tlssecpmlkem=0
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
