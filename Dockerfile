# Build release image
FROM alpine:latest

RUN apk add --no-cache bash ca-certificates

RUN adduser \
    --disabled-password \
    --gecos "" \
    --home "/nonexistent" \
    --shell "/sbin/nologin" \
    --no-create-home \
    --uid "1000" \
    agent

COPY assets-deploy /usr/bin/assets-deploy

USER agent:agent
CMD ["/usr/bin/assets-deploy"]
