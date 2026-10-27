# SPDX-License-Identifier: AGPL-3.0-only
# The container image: the binary GoReleaser built, on a distroless base, run
# as a user without rights. The database lives in /home/nonroot; mount a
# volume there. The base is pinned by digest; Dependabot proposes updates.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/allstill /usr/bin/allstill
ENV ALLSTILL_DB=/home/nonroot/allstill.db \
    ALLSTILL_LISTEN=0.0.0.0:8000
VOLUME /home/nonroot
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/usr/bin/allstill"]
CMD ["serve"]
