# oblireach-rdv - rendezvous server of Oblireach Support.
#
# Build from the repository root (the server shares its wire protocol with
# supportlink/wire):
#
#   docker build -f rdv/Dockerfile -t oblireach-rdv .
#
# rdv/Dockerfile.dockerignore keeps the build context to rdv/ and supportlink/
# (BuildKit). The classic builder ignores it and sends the whole repository.
#
# Pure Go (SQLite included: modernc.org/sqlite), CGO_ENABLED=0: the binary is
# static and runs on distroless/static.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY supportlink/go.mod supportlink/go.sum supportlink/
COPY rdv/go.mod rdv/go.sum rdv/
RUN cd rdv && go mod download
COPY supportlink/ supportlink/
COPY rdv/ rdv/
ARG VERSION=dev
RUN cd rdv && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/oblireach-rdv ./cmd/oblireach-rdv \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oblireach-rdv /oblireach-rdv
COPY --from=build --chown=65532:65532 /out/data /data
# Bind 443 as a non-root user: the file capability cap_net_bind_service is
# set HERE, in the final stage (exec form, distroless has no shell). Set in
# the build stage, it would be lost: COPY --from with the classic builder
# drops extended attributes. Needs cap_add NET_BIND_SERVICE at run time
# (and no "no-new-privileges", which disables file capabilities).
USER 0:0
RUN ["/oblireach-rdv", "setcap-self"]
USER 65532:65532
ENV RDV_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 443/tcp 3478/udp 3478/tcp
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/oblireach-rdv", "healthcheck"]
ENTRYPOINT ["/oblireach-rdv"]
