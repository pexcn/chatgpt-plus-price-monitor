FROM golang:1.26-alpine AS builder

COPY . .
RUN apk add --no-cache --virtual .build-deps git make \
  && make \
  && make install \
  && apk del .build-deps \
  && rm -rf /var/cache/apk/*

FROM pexcn/docker-images:scratch
LABEL maintainer="pexcn <pexcn97@gmail.com>"

COPY --from=builder /usr/local/bin/chatgpt-plus-price-monitor /usr/local/bin/

ENTRYPOINT ["chatgpt-plus-price-monitor"]
