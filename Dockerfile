# tracker — a single static binary on a distroless base.
#
# Nothing in the runtime image but the binary: tracker shells out to nothing, and the
# migrations are embedded in it (internal/db), so there is no migrations directory to
# ship alongside it and no schema/binary skew to manage.

FROM golang:1.26.8-bookworm AS build
WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tracker ./cmd/tracker

# distroless: no shell, no package manager, no setuid binaries. Runs as nonroot — tracker
# listens on :8080, not a privileged port, so it never needs to be root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tracker /usr/local/bin/tracker
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tracker"]
