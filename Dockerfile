# Build release image
FROM alpine:latest@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

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
