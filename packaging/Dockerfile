# SPDX-License-Identifier: AGPL-3.0-only
# The container image: the binary GoReleaser built, on a distroless base, run
# as a user without rights. The database lives in /home/nonroot; mount a
# volume there. The base is pinned by digest; Dependabot proposes updates.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/allstill /usr/bin/allstill
ENV ALLSTILL_DB=/home/nonroot/allstill.db \
    ALLSTILL_LISTEN=0.0.0.0:8000
VOLUME /home/nonroot
EXPOSE 8000
USER nonroot:nonroot
ENTRYPOINT ["/usr/bin/allstill"]
CMD ["serve"]
