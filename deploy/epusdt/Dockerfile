ARG GO_IMAGE=golang@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440
ARG RUNTIME_IMAGE=alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
FROM ${GO_IMAGE} AS builder
ENV CGO_ENABLED=0
WORKDIR /source
COPY --from=epusdt_source /src/ ./
COPY bootstrap/*.go ./fcard-bootstrap/
RUN go mod download
RUN go test ./fcard-bootstrap
RUN go build -trimpath -ldflags="-s -w -X github.com/GMWalletApp/epusdt/config.BuildVersion=v2.0.0 -X github.com/GMWalletApp/epusdt/config.BuildCommit=619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3" -o /out/epusdt .
RUN go build -trimpath -ldflags="-s -w" -o /out/fcard-bootstrap ./fcard-bootstrap

FROM ${RUNTIME_IMAGE}
WORKDIR /app
LABEL org.opencontainers.image.source="https://github.com/GMWalletApp/epusdt"
LABEL org.opencontainers.image.revision="619cecb11d6bbd1dcdd1b698ee08b37fc722d3e3"
COPY --from=builder /out/epusdt /out/fcard-bootstrap /app/
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod 755 /app/entrypoint.sh
ENTRYPOINT ["/app/entrypoint.sh"]
