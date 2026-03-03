ARG GO_VERSION=1.25
ARG IMG_TAG=latest

# Compile provider and consumer binaries
FROM golang:${GO_VERSION}-alpine AS testapp-builder

RUN apk add --no-cache git openssh ca-certificates build-base linux-headers

WORKDIR /src/

# Copy root module (vaas types/modules)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build app binaries
WORKDIR /src/app/
RUN go mod download
RUN CGO_ENABLED=0 go build -o /usr/local/bin/provider ./cmd/provider
RUN CGO_ENABLED=0 go build -o /usr/local/bin/consumer ./cmd/consumer

# Final image with both binaries and common tools
FROM alpine:${IMG_TAG}

RUN apk add --no-cache bash curl jq sed

RUN adduser -D nonroot -h /home/nonroot

COPY --from=testapp-builder /usr/local/bin/provider /usr/local/bin/provider
COPY --from=testapp-builder /usr/local/bin/consumer /usr/local/bin/consumer

EXPOSE 26656 26657 26658 1317 9090

USER nonroot

# No fixed entrypoint — the test suite specifies the command
ENTRYPOINT []
