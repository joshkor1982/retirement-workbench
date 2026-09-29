# Army Retirement Workbench container image.
#
#   docker compose up -d        # then open http://localhost:5252
#
# Your data lives in the /data volume, never in the image, so it survives the
# container being stopped, removed, or upgraded. The Advisor is off in the
# container (it needs the Claude or ChatGPT app signed in on your computer);
# run the standalone app if you want it.

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY templates ./templates
COPY static ./static
COPY tools ./tools
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/arw .

FROM debian:bookworm-slim
# poppler-utils: pdfunite, which combines packet PDFs on Linux.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates poppler-utils tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /data --shell /usr/sbin/nologin workbench \
 && mkdir -p /data && chown 10001:10001 /data
COPY --from=build /out/arw /usr/local/bin/arw
USER 10001
ENV RW_DATA=/data RW_ADDR=0.0.0.0:5252 RW_OPEN_BROWSER=0
VOLUME /data
EXPOSE 5252
HEALTHCHECK --interval=30s --timeout=3s CMD ["arw", "-healthcheck"]
ENTRYPOINT ["arw"]
