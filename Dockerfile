# Build stage — no Go needed on the host, only Docker.
FROM golang:1.26-bookworm AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# Pure-Go SQLite (modernc) => fully static binary, no CGO.
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /winnow ./cmd/winnow

# Create the data dir with the runtime user's ownership so a freshly-created
# named volume mounted here is writable by the non-root process.
RUN mkdir -p /data && chown 65532:65532 /data

# Runtime stage — distroless static: CA certs included, runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /winnow /winnow
COPY --from=build --chown=65532:65532 /data /data
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/winnow"]
