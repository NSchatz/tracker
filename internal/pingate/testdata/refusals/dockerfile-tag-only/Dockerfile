# DELIBERATELY BROKEN. Do not fix.
#
# The builder base carries a tag and no digest, which is what every Dockerfile in this org looked
# like before the pinning conventions landed. The tag reads reassuringly exact and is still moved
# by its publisher whenever they rebuild it, so this line does not say what it will build on.
# internal/pingate must refuse it under P2.
FROM golang:1.26.8-bookworm AS build
WORKDIR /src
RUN echo "nothing is built here; this tree exists to be refused"

# The runtime base IS correctly pinned, so the refusal above cannot be an accident of the scanner
# rejecting everything it sees.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /src /srv
