FROM alpine:3.22

ARG TARGETARCH

RUN apk add --no-cache ca-certificates && \
    addgroup -S appgroup && \
    adduser -S appuser -G appgroup

COPY --chown=appuser:appgroup bin/linux/${TARGETARCH}/app /app

USER appuser
ENTRYPOINT ["/app"]