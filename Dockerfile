# tracker — a single static binary on a distroless base.
#
# Nothing in the runtime image but the binary: tracker shells out to nothing, and the
# migrations are embedded in it (internal/db), so there is no migrations directory to
# ship alongside it and no schema/binary skew to manage.

# Pinned by TAG AND DIGEST (pinning conventions P1/P2). The tag stays readable to a
# human; the digest is what actually resolves, so the compiler that builds the shipped
# binary cannot change under a rebuild. Provenance and the refresh command for every
# digest in this repo are in README.md, "Pinned references".
FROM golang:1.26.8-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build
WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tracker ./cmd/tracker

# distroless: no shell, no package manager, no setuid binaries. Runs as nonroot — tracker
# listens on :8080, not a privileged port, so it never needs to be root.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/tracker /usr/local/bin/tracker
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tracker"]
