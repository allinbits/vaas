# The forge e2e image: the e2e chain image plus the lying consumer node
# (consumer-forge), for the byzantine-consumer suite only. Built with the
# repository root as context by `make docker-build-forge`; the other e2e
# suites never build it.
ARG GO_VERSION=1.26.0

FROM golang:${GO_VERSION}-alpine AS forge-builder

RUN apk add --no-cache git ca-certificates build-base linux-headers

WORKDIR /src/
COPY . .

WORKDIR /src/tests/e2e/forge/
RUN go mod download
RUN CGO_ENABLED=0 go build -o /usr/local/bin/consumer-forge .

FROM cosmos/vaas-e2e

COPY --from=forge-builder /usr/local/bin/consumer-forge /usr/local/bin/consumer-forge
