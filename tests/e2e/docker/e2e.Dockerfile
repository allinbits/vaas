ARG GO_VERSION=1.26.0
ARG IMG_TAG=3.22
# The e2e chains vote in 30 s. atomone-sdk's gov module refuses a voting
# period under 21 days at genesis validation unless its floor is overridden
# at build time, which is what a test network does.
ARG MIN_VOTING_PERIOD=30s

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
ARG MIN_VOTING_PERIOD
RUN CGO_ENABLED=0 go build -ldflags "-X github.com/cosmos/cosmos-sdk/x/gov/types/v1.MinVotingPeriod=${MIN_VOTING_PERIOD}" -o /usr/local/bin/provider ./cmd/provider
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
