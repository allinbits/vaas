# The forge e2e image: the e2e chain image plus the lying consumer node
# (consumer-forge), for the byzantine-consumer suite only. Built with the
# repository root as context by `make docker-build-forge`; the other e2e
# suites never build it.
ARG GO_VERSION=1.26.11

FROM golang:${GO_VERSION}-alpine AS forge-builder

RUN apk add --no-cache git ca-certificates build-base linux-headers

# Module files first, so the dependency download is cached until one of the
# three modules the forge builds against changes its requirements.
WORKDIR /src/
COPY go.mod go.sum ./
COPY app/go.mod app/go.sum ./app/
COPY tests/e2e/forge/go.mod tests/e2e/forge/go.sum ./tests/e2e/forge/
WORKDIR /src/tests/e2e/forge/
RUN go mod download

WORKDIR /src/
COPY . .
WORKDIR /src/tests/e2e/forge/
RUN CGO_ENABLED=0 go build -o /usr/local/bin/consumer-forge .

FROM cosmos/vaas-e2e

COPY --from=forge-builder /usr/local/bin/consumer-forge /usr/local/bin/consumer-forge
